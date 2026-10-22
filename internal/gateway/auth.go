package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Authenticator validates client API keys. Keys are looked up in the
// Authorization bearer token, then "x-api-key", then "api-key".
type Authenticator struct {
	keys [][32]byte
}

func NewAuthenticator(keys []string) *Authenticator {
	a := &Authenticator{}
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			a.keys = append(a.keys, sha256.Sum256([]byte(k)))
		}
	}
	return a
}

// Enabled reports whether any key is configured. With none, access is open.
func (a *Authenticator) Enabled() bool { return len(a.keys) > 0 }

func (a *Authenticator) Authorize(r *http.Request) bool {
	if !a.Enabled() {
		return true
	}
	cred := credential(r)
	if cred == "" {
		return false
	}
	sum := sha256.Sum256([]byte(cred))
	ok := 0
	for _, k := range a.keys { // compare all keys: constant work
		ok |= subtle.ConstantTimeCompare(sum[:], k[:])
	}
	return ok == 1
}

func credential(r *http.Request) string {
	if v := r.Header.Get("Authorization"); v != "" {
		if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
			return strings.TrimSpace(v[7:])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Api-Key"); v != "" {
		return v
	}
	return r.Header.Get("Api-Key")
}
