package tui

import (
	"net/http"

	"github.com/rnapoles/llmux/internal/inspector"
)

// Everything below is attacker-influenced (model names, URLs, header values,
// upstream error text...). It is normalised once, at the boundary, so no
// rendering code has to worry about it.

func cleanSummary(s inspector.Summary) inspector.Summary {
	s.Method, s.Path, s.VHost = inline(s.Method), inline(s.Path), inline(s.VHost)
	s.Dialect, s.Backend, s.Operation = inline(s.Dialect), inline(s.Backend), inline(s.Operation)
	s.Mode, s.Model, s.Search = inline(s.Mode), inline(s.Model), inline(s.Search)
	return s
}

func cleanHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for k, vv := range h {
		nv := make([]string, len(vv))
		for i, v := range vv {
			nv[i] = inline(v)
		}
		out[inline(k)] = nv
	}
	return out
}

func cleanRecord(r inspector.Record) inspector.Record {
	r.ClientAddr, r.Host, r.VHost = inline(r.ClientAddr), inline(r.Host), inline(r.VHost)
	r.Dialect, r.Backend, r.Operation = inline(r.Dialect), inline(r.Backend), inline(r.Operation)
	r.Mode, r.Model, r.Error = inline(r.Mode), inline(r.Model), inline(r.Error)
	for _, l := range []*inspector.Leg{&r.Client, &r.Upstream} {
		l.Method, l.URL = inline(l.Method), inline(l.URL)
		l.ReqHeader, l.RespHeader = cleanHeader(l.ReqHeader), cleanHeader(l.RespHeader)
	}
	return r
}
