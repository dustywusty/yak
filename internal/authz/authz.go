// Package authz evaluates Abby channel policy: default deny, deny overrides
// allow, private channels as allow-inheritance boundaries, and credential
// scope ceilings. It is pure so it can be tested exhaustively (ADR-002).
package authz

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Channel capabilities (spec/protocol.md AUTH-010).
const (
	Read          = "read"
	Post          = "post"
	React         = "react"
	Label         = "label"
	Claim         = "claim"
	TaskManage    = "task_manage"
	TaskAdmin     = "task_admin"
	ChannelManage = "channel_manage"
	// BoardAdmin is a board role. As a grant it is only valid with pattern "*".
	BoardAdmin = "board_admin"
	// ServerAdmin is an actor attribute; as a credential scope it permits the
	// credential to exercise that attribute.
	ServerAdmin = "server_admin"
	// AllScopes is the credential scope meaning "no ceiling beyond the actor".
	AllScopes = "*"
)

var ChannelCapabilities = []string{Read, Post, React, Label, Claim, TaskManage, TaskAdmin, ChannelManage}

// ValidScope reports whether s may appear in a credential's scope list.
func ValidScope(s string) bool {
	return s == AllScopes || s == BoardAdmin || s == ServerAdmin || slices.Contains(ChannelCapabilities, s)
}

// ValidGrantCapability reports whether c may appear in a grant.
func ValidGrantCapability(c string) bool {
	return c == BoardAdmin || slices.Contains(ChannelCapabilities, c)
}

var segmentRE = regexp.MustCompile(`^[a-z0-9_-]+$`)

// MaxPathBytes is the total byte limit of a channel path.
const MaxPathBytes = 255

// ValidatePath checks a channel path: lowercase ASCII segments of
// [a-z0-9_-] separated by single "/", 1..255 bytes.
func ValidatePath(p string) error {
	if p == "" || len(p) > MaxPathBytes {
		return fmt.Errorf("channel path must be 1..%d bytes", MaxPathBytes)
	}
	for _, seg := range strings.Split(p, "/") {
		if !segmentRE.MatchString(seg) {
			return fmt.Errorf("channel path segment %q must match [a-z0-9_-]+", seg)
		}
	}
	return nil
}

// ValidatePattern checks a grant pattern: "*", an exact path, or "path/*".
func ValidatePattern(p string) error {
	if p == "*" {
		return nil
	}
	if base, ok := strings.CutSuffix(p, "/*"); ok {
		return ValidatePath(base)
	}
	return ValidatePath(p)
}

// Matches reports whether grant pattern pat applies to channel path.
// "*" matches every channel; "a/*" matches strict descendants of "a" only
// (not "a" itself, not "a-b"); anything else is an exact match.
func Matches(pat, path string) bool {
	if pat == "*" {
		return true
	}
	if base, ok := strings.CutSuffix(pat, "/*"); ok {
		return strings.HasPrefix(path, base+"/")
	}
	return pat == path
}

// InSubtree reports whether path equals root or is a descendant of root.
func InSubtree(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// Grant is one applicable grant (already filtered to the actor's subject).
type Grant struct {
	ID      string
	Pattern string
	Caps    []string
	Effect  string // "allow" | "deny"
}

// Policy is the evaluated policy for one actor+credential on one board.
type Policy struct {
	IsOwner      bool
	Grants       []Grant
	PrivatePaths []string // every private channel path on the board
	// Credential ceiling.
	Scopes       []string
	BoardInScope bool // false if the credential's board_ids excludes this board
}

func (p *Policy) scoped(c string) bool {
	if !p.BoardInScope {
		return false
	}
	return slices.Contains(p.Scopes, AllScopes) || slices.Contains(p.Scopes, c)
}

// Boundary returns the deepest private channel path whose subtree contains
// path, or "" when no private boundary applies.
func (p *Policy) Boundary(path string) string {
	best := ""
	for _, b := range p.PrivatePaths {
		if InSubtree(b, path) && len(b) > len(best) {
			best = b
		}
	}
	return best
}

// patternWithin reports whether every channel a pattern can match lies within
// subtree(boundary). "*" never qualifies.
func patternWithin(pat, boundary string) bool {
	if pat == "*" {
		return false
	}
	base := strings.TrimSuffix(pat, "/*")
	return InSubtree(boundary, base)
}

// Has reports whether the actor+credential holds capability c on path.
func (p *Policy) Has(path, c string) bool {
	if !p.scoped(c) {
		return false
	}
	boundary := p.Boundary(path)
	allowed := false
	for _, g := range p.Grants {
		if !slices.Contains(g.Caps, c) || !Matches(g.Pattern, path) {
			continue
		}
		if g.Effect == "deny" {
			return false
		}
		if boundary == "" || patternWithin(g.Pattern, boundary) {
			allowed = true
		}
	}
	return allowed
}

// Caps returns all channel capabilities held on path, sorted.
func (p *Policy) Caps(path string) []string {
	out := []string{}
	for _, c := range ChannelCapabilities {
		if p.Has(path, c) {
			out = append(out, c)
		}
	}
	return out
}

// IsBoardAdmin reports the board administration role: (owner or an allow
// grant of board_admin) and no deny of board_admin, within the credential
// ceiling. Ownership does not override a deny (ADR-002).
func (p *Policy) IsBoardAdmin() bool {
	if !p.scoped(BoardAdmin) {
		return false
	}
	allowed := p.IsOwner
	for _, g := range p.Grants {
		if !slices.Contains(g.Caps, BoardAdmin) {
			continue
		}
		if g.Effect == "deny" {
			return false
		}
		allowed = true
	}
	return allowed
}

// HasAnyAllow reports whether the actor holds any allow grant on the board
// within the credential ceiling; used for board metadata visibility.
func (p *Policy) HasAnyAllow() bool {
	if !p.BoardInScope {
		return false
	}
	for _, g := range p.Grants {
		if g.Effect == "allow" {
			return true
		}
	}
	return false
}

// Fingerprint identifies the effective content policy for this actor and
// credential. A change invalidates cursors and snapshot sessions bound to it
// (ADR-006). Channel creation of an inherited channel does not change it.
func (p *Policy) Fingerprint() string {
	var parts []string
	for _, g := range p.Grants {
		caps := slices.Clone(g.Caps)
		sort.Strings(caps)
		parts = append(parts, "g:"+g.ID+":"+g.Pattern+":"+g.Effect+":"+strings.Join(caps, ","))
	}
	priv := slices.Clone(p.PrivatePaths)
	sort.Strings(priv)
	for _, x := range priv {
		parts = append(parts, "p:"+x)
	}
	sc := slices.Clone(p.Scopes)
	sort.Strings(sc)
	parts = append(parts, "s:"+strings.Join(sc, ","), fmt.Sprintf("o:%v:%v", p.IsOwner, p.BoardInScope))
	sort.Strings(parts)
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(h[:8])
}
