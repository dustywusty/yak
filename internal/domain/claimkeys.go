package domain

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ClaimKeyring holds the operator keys that encrypt claim-proof replay
// material (ADR-0004 §3, CLAIM-043). Keys never reach the database; the
// first key encrypts, every key decrypts (rotation).
type ClaimKeyring struct {
	primary string
	keys    map[string][]byte
}

// ParseClaimKeyring reads "id:base64key[,id:base64key...]". Each key is 32
// bytes (AES-256). A bare base64 key (no "id:") gets the ID "k1".
func ParseClaimKeyring(spec string) (*ClaimKeyring, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	kr := &ClaimKeyring{keys: map[string][]byte{}}
	for i, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		id, enc, ok := strings.Cut(part, ":")
		if !ok {
			id, enc = "k"+strconv.Itoa(i+1), part
		}
		if id == "" || strings.ContainsAny(id, " \t") {
			return nil, fmt.Errorf("claim key %d: empty or invalid key id", i+1)
		}
		key, err := decodeKey(enc)
		if err != nil {
			return nil, fmt.Errorf("claim key %q: %w", id, err)
		}
		if _, dup := kr.keys[id]; dup {
			return nil, fmt.Errorf("claim key %q listed twice", id)
		}
		kr.keys[id] = key
		if kr.primary == "" {
			kr.primary = id
		}
	}
	return kr, nil
}

func decodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != 32 {
				return nil, errors.New("key must be 32 bytes (AES-256), base64-encoded")
			}
			return b, nil
		}
	}
	return nil, errors.New("key must be base64")
}

func claimAAD(boardID, taskID, claimID, actorID string, epoch int64) []byte {
	return []byte(boardID + "\x00" + taskID + "\x00" + claimID + "\x00" + actorID + "\x00" + strconv.FormatInt(epoch, 10))
}

func (k *ClaimKeyring) seal(secret string, aad []byte) ([]byte, string, error) {
	g, err := gcmFor(k.keys[k.primary])
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	return g.Seal(nonce, nonce, []byte(secret), aad), k.primary, nil
}

func (k *ClaimKeyring) open(ct []byte, keyID string, aad []byte) (string, error) {
	key, ok := k.keys[keyID]
	if !ok {
		return "", errors.New("claim key no longer configured")
	}
	g, err := gcmFor(key)
	if err != nil {
		return "", err
	}
	if len(ct) < g.NonceSize() {
		return "", errors.New("short ciphertext")
	}
	pt, err := g.Open(nil, ct[:g.NonceSize()], ct[g.NonceSize():], aad)
	return string(pt), err
}

func gcmFor(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// ---- proofs ----

const proofPrefix = "acp1."

var proofRE = regexp.MustCompile(`^acp1\.([A-Za-z0-9_]{1,64})\.([A-Za-z0-9_-]{43})$`)

// ClaimProof is a parsed Abby-Claim-Proof header: acp1.<claim_id>.<secret>.
type ClaimProof struct {
	ClaimID string
	secret  string
}

// ParseClaimProof validates the proof syntax (CLAIM-013: malformed is 400).
func ParseClaimProof(s string) (*ClaimProof, error) {
	m := proofRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		e := Validation("Abby-Claim-Proof is malformed")
		e.Extra = map[string]any{"header": "Abby-Claim-Proof"}
		return nil, e
	}
	return &ClaimProof{ClaimID: m[1], secret: m[2]}, nil
}

func newProofSecret() (string, error) {
	b := make([]byte, 32) // 256 bits (CLAIM-010)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func proofVerifier(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

func (c *ClaimProof) verifies(verifier []byte) bool {
	return subtle.ConstantTimeCompare(proofVerifier(c.secret), verifier) == 1
}
