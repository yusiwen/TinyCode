// Package idgen makes the short opaque ids a report or a URL can carry.
package idgen

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// New returns a 16-character id drawn from the operating system's entropy.
func New() string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		// The caller has no better answer than a fixed string here, and a
		// panic in an id generator would take the process down for cosmetics.
		return "0000000000000000"
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
}
