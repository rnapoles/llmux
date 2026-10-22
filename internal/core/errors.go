package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// UpstreamError is returned by backends when the upstream answered with a
// non-success HTTP status.
type UpstreamError struct {
	Status  int
	Message string
	Body    []byte
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream returned HTTP %d: %s", e.Status, e.Message)
}

// BadRequestError marks client mistakes (malformed payloads).
type BadRequestError struct{ Msg string }

func (e *BadRequestError) Error() string { return e.Msg }

// NewID returns a random identifier such as "chatcmpl-9f2c...".
func NewID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
