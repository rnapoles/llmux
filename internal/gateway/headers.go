package gateway

import (
	"net/http"
	"os"
	"strings"

	"github.com/rnapoles/llmux/internal/config"
)

// Headers that must never be copied between connections.
var alwaysBlocked = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true,
	"proxy-connection": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
	"host": true, "content-length": true, "accept-encoding": true,
}

// Credentials of the *client↔gateway* hop; they are only forwarded when the
// operator explicitly lists them in `forward`.
var credentialHeaders = map[string]bool{
	"authorization": true, "x-api-key": true, "api-key": true, "cookie": true,
}

// HeaderPolicy implements header forwarding, removal and overriding.
type HeaderPolicy struct {
	forward []string
	remove  []string
	set     map[string]string
}

func NewHeaderPolicy(r config.HeaderRules) *HeaderPolicy {
	p := &HeaderPolicy{set: map[string]string{}}
	for _, f := range r.Forward {
		p.forward = append(p.forward, strings.ToLower(strings.TrimSpace(f)))
	}
	for _, f := range r.Remove {
		p.remove = append(p.remove, strings.ToLower(strings.TrimSpace(f)))
	}
	for k, v := range r.Set {
		p.set[http.CanonicalHeaderKey(strings.TrimSpace(k))] = os.ExpandEnv(v)
	}
	return p
}

// match supports exact names, "prefix*" and "*".
func match(patterns []string, name string) bool {
	name = strings.ToLower(name)
	for _, p := range patterns {
		switch {
		case p == "*" || p == name:
			return true
		case strings.HasSuffix(p, "*") && strings.HasPrefix(name, strings.TrimSuffix(p, "*")):
			return true
		}
	}
	return false
}

// BuildRequest computes the outbound request headers from the inbound ones:
// forward (allow-list or default deny-list) → remove → set.
func (p *HeaderPolicy) BuildRequest(in http.Header) http.Header {
	out := http.Header{}
	for k, vv := range in {
		lk := strings.ToLower(k)
		if alwaysBlocked[lk] {
			continue
		}
		explicit := match(p.forward, lk)
		if len(p.forward) > 0 && !explicit {
			continue
		}
		if credentialHeaders[lk] && !explicit {
			continue
		}
		for _, v := range vv {
			out.Add(k, v)
		}
	}
	p.mutate(out)
	return out
}

// FilterResponse applies the rules to response headers in place. When a
// forward list is given, headers outside it (except Content-Type) are dropped.
func (p *HeaderPolicy) FilterResponse(h http.Header) {
	if len(p.forward) > 0 {
		for k := range h {
			if !match(p.forward, k) && !strings.EqualFold(k, "Content-Type") {
				delete(h, k)
			}
		}
	}
	p.mutate(h)
}

func (p *HeaderPolicy) mutate(h http.Header) {
	for k := range h {
		if match(p.remove, k) {
			delete(h, k)
		}
	}
	for k, v := range p.set {
		if v == "" {
			h.Del(k)
		} else {
			h.Set(k, v)
		}
	}
}
