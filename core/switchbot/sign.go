// Package switchbot is the SwitchBot Open API v1.1 client.
//
// The API is read-only from this package's point of view: it lists devices and
// reads their status. Sending commands to devices is deliberately not
// implemented — sensor-lens never actuates anything (see AGENTS.md).
package switchbot

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// Sign builds the value of the "sign" header.
//
// The API signs the concatenation token+timestamp+nonce with HMAC-SHA256 keyed
// by the secret, base64-encodes it, and upper-cases the result. It is a pure
// function of its inputs so it can be verified against a fixed vector.
func Sign(token, secret string, timestampMillis int64, nonce string) string {
	data := fmt.Sprintf("%s%d%s", token, timestampMillis, nonce)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data)) // hash.Hash.Write never returns an error
	return strings.ToUpper(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

// newNonce returns a random UUIDv4 string.
//
// Hand-rolled rather than pulled from a dependency so this package stays
// dependency-free; the layout follows RFC 9562 §5.4 (the same 16 bytes the
// official Go sample produces).
func newNonce() (string, error) {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant RFC 9562

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]), nil
}
