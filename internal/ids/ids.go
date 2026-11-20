// Package ids mints opaque resource identifiers. Clients must not interpret
// them; the prefix is a debugging aid, not part of the protocol.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// New returns prefix + "_" + 26 chars of base32 randomness (128 bits).
func New(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + enc.EncodeToString(b)
}

// Secret returns a 256-bit random secret in lowercase base32.
func Secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(enc.EncodeToString(b))
}
