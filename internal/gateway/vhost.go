package gateway

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/rnapoles/llmux/internal/config"
	"github.com/rnapoles/llmux/internal/core"
)

// DialectSource and BackendSource are the registries the gateway depends on
// (Dependency Inversion): any provider of dialects/backends can be plugged in.
type DialectSource interface {
	Get(name string) (core.Dialect, error)
	Names() []string
}

type BackendSource interface {
	Build(typ string, spec core.BackendSpec) (core.Backend, error)
}

// VHost is one virtual host: dialects in, one backend out.
type VHost struct {
	Name      string
	Patterns  []string
	Dialects  []core.Dialect
	Backend   core.Backend
	Auth      *Authenticator
	ModelMap  map[string]string
	Req, Resp *HeaderPolicy
	Transport http.RoundTripper
}

func newVHost(cfg config.Host, dialects DialectSource, backends BackendSource) (*VHost, error) {
	names := cfg.Dialects
	if len(names) == 0 {
		names = dialects.Names()
	}
	v := &VHost{
		Name:     cfg.Name,
		Patterns: cfg.Hosts,
		Auth:     NewAuthenticator(cfg.APIKeys),
		ModelMap: cfg.ModelMap,
		Req:      NewHeaderPolicy(cfg.Headers.Request),
		Resp:     NewHeaderPolicy(cfg.Headers.Response),
	}
	for _, n := range names {
		d, err := dialects.Get(strings.ToLower(n))
		if err != nil {
			return nil, fmt.Errorf("host %q: %w", cfg.Name, err)
		}
		v.Dialects = append(v.Dialects, d)
	}

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = cfg.Upstream.Timeout
	base.MaxIdleConnsPerHost = 32
	if cfg.Upstream.InsecureSkipVerify {
		base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in
	}
	v.Transport = &inspectTransport{base: base}

	u, _ := url.Parse(cfg.Upstream.URL)
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	be, err := backends.Build(cfg.Upstream.Type, core.BackendSpec{
		BaseURL: u,
		APIKey:  cfg.Upstream.APIKey,
		Client:  &http.Client{Transport: v.Transport},
	})
	if err != nil {
		return nil, fmt.Errorf("host %q: %w", cfg.Name, err)
	}
	v.Backend = be
	return v, nil
}

// route finds the dialect (and neutral operation) for a request.
func (v *VHost) route(r *http.Request) (core.Dialect, core.Operation) {
	for _, d := range v.Dialects {
		if op, ok := d.Match(r); ok {
			return d, op
		}
	}
	return nil, ""
}

func (v *VHost) mapModel(m string) string {
	if real, ok := v.ModelMap[m]; ok {
		return real
	}
	return m
}

// rewriteModel applies model_map to a raw JSON body (transparent proxy mode).
func (v *VHost) rewriteModel(body []byte) []byte {
	if len(v.ModelMap) == 0 || len(body) == 0 || body[0] != '{' {
		return body
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	var model string
	if json.Unmarshal(obj["model"], &model) != nil {
		return body
	}
	real, ok := v.ModelMap[model]
	if !ok {
		return body
	}
	obj["model"], _ = json.Marshal(real)
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// upstreamHeaders builds the outbound header set: policy then provider auth.
func (v *VHost) upstreamHeaders(in http.Header) http.Header {
	h := v.Req.BuildRequest(in)
	v.Backend.PrepareHeaders(h)
	return h
}

// serveProxy forwards the request unchanged (client dialect == backend dialect).
func (v *VHost) serveProxy(w http.ResponseWriter, r *http.Request, d core.Dialect, body []byte) {
	body = v.rewriteModel(body)
	target := v.Backend.Endpoint()
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header = v.upstreamHeaders(r.Header)
			if len(body) > 0 {
				pr.Out.Body = io.NopCloser(bytes.NewReader(body))
			} else {
				pr.Out.Body = http.NoBody
			}
			pr.Out.ContentLength = int64(len(body))
		},
		Transport:     v.Transport,
		FlushInterval: -1, // flush immediately: required for token streaming
		ModifyResponse: func(resp *http.Response) error {
			v.Resp.FilterResponse(resp.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			status := http.StatusBadGateway
			if errors.Is(err, http.ErrHandlerTimeout) {
				status = http.StatusGatewayTimeout
			}
			d.WriteError(w, status, fmt.Errorf("upstream error: %w", err))
		},
	}
	rp.ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// Host table
// ---------------------------------------------------------------------------

type hostTable struct {
	exact    map[string]*VHost
	wild     []wildcard // longest suffix first
	fallback *VHost
}

type wildcard struct {
	suffix string // ".example.com"
	vhost  *VHost
}

func newHostTable(vhosts []*VHost) (*hostTable, error) {
	t := &hostTable{exact: map[string]*VHost{}}
	for _, v := range vhosts {
		for _, p := range v.Patterns {
			switch {
			case p == "*":
				if t.fallback == nil {
					t.fallback = v
				}
			case strings.HasPrefix(p, "*."):
				t.wild = append(t.wild, wildcard{suffix: p[1:], vhost: v})
			default:
				if _, dup := t.exact[p]; dup {
					return nil, fmt.Errorf("host name %q is declared by more than one virtual host", p)
				}
				t.exact[p] = v
			}
		}
	}
	sort.Slice(t.wild, func(i, j int) bool { return len(t.wild[i].suffix) > len(t.wild[j].suffix) })
	return t, nil
}

func (t *hostTable) lookup(hostport string) *VHost {
	host := strings.ToLower(hostport)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if v, ok := t.exact[host]; ok {
		return v
	}
	for _, w := range t.wild {
		if strings.HasSuffix(host, w.suffix) {
			return w.vhost
		}
	}
	return t.fallback
}
