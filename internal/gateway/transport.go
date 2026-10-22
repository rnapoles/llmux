package gateway

import (
	"io"
	"net/http"

	"github.com/rnapoles/llmux/internal/inspector"
)

// inspectTransport records the LLMux→upstream leg of every exchange whose
// context carries an *inspector.Exchange.
type inspectTransport struct {
	base   http.RoundTripper
	redact bool
}

func (t *inspectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ex := inspector.FromContext(req.Context())
	if ex == nil {
		return t.base.RoundTrip(req)
	}
	h := req.Header.Clone()
	if t.redact {
		h = inspector.RedactHeaders(h)
	}
	ex.Update(func(r *inspector.Record) {
		r.Upstream.Method = req.Method
		r.Upstream.URL = req.URL.String()
		r.Upstream.ReqHeader = h
	})
	out := req.Clone(req.Context()) // RoundTrippers must not mutate the caller's request
	if req.Body != nil && req.Body != http.NoBody {
		out.Body = &tee{rc: req.Body, sink: func(p []byte) {
			ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Upstream.ReqBody }, p)
		}}
	}
	resp, err := t.base.RoundTrip(out)
	if err != nil {
		ex.Update(func(r *inspector.Record) { r.Error = "upstream: " + err.Error() })
		return nil, err
	}
	rh := resp.Header.Clone()
	if t.redact {
		rh = inspector.RedactHeaders(rh)
	}
	ex.Update(func(r *inspector.Record) {
		r.Upstream.Status = resp.StatusCode
		r.Upstream.RespHeader = rh
	})
	resp.Body = &tee{rc: resp.Body, sink: func(p []byte) {
		ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Upstream.RespBody }, p)
	}}
	return resp, nil
}

// tee copies everything read from rc into sink.
type tee struct {
	rc   io.ReadCloser
	sink func([]byte)
}

func (t *tee) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 {
		t.sink(p[:n])
	}
	return n, err
}

func (t *tee) Close() error { return t.rc.Close() }
