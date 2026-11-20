package authz

import "testing"

func TestMatchesBoundaries(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"*", "a", true},
		{"engineering/*", "engineering/x", true},
		{"engineering/*", "engineering/x/y", true},
		{"engineering/*", "engineering", false},
		{"engineering/*", "engineering-tools", false},
		{"engineering/*", "engineering-tools/x", false},
		{"engineering", "engineering", true},
		{"engineering", "engineering/x", false},
	}
	for _, c := range cases {
		if got := Matches(c.pat, c.path); got != c.want {
			t.Errorf("Matches(%q,%q)=%v want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestValidatePath(t *testing.T) {
	good := []string{"a", "a/b", "eng_x/sec-1"}
	bad := []string{"", "A", "a//b", "/a", "a/", "a b", "é"}
	for _, p := range good {
		if err := ValidatePath(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, p := range bad {
		if ValidatePath(p) == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

// The worked example from design section 4.1.
func TestPrivateBoundaryExample(t *testing.T) {
	member := Grant{ID: "g1", Pattern: "*", Caps: []string{Read, Post}, Effect: "allow"}
	aliceSec := Grant{ID: "g2", Pattern: "engineering/security", Caps: []string{Read, Post}, Effect: "allow"}
	priv := []string{"engineering/security"}

	bob := Policy{Grants: []Grant{member}, PrivatePaths: priv, Scopes: []string{"*"}, BoardInScope: true}
	alice := Policy{Grants: []Grant{member, aliceSec}, PrivatePaths: priv, Scopes: []string{"*"}, BoardInScope: true}
	aliceReadOnly := alice
	aliceReadOnly.Scopes = []string{Read}

	if !bob.Has("general", Read) || !bob.Has("engineering", Post) {
		t.Fatal("member should read/post outside private boundary")
	}
	if bob.Has("engineering/security", Read) || bob.Has("engineering/security/x", Read) {
		t.Fatal("board-wide allow must not cross private boundary")
	}
	if !alice.Has("engineering/security", Post) {
		t.Fatal("explicit allow inside boundary should apply")
	}
	if alice.Has("engineering/security/x", Read) {
		t.Fatal("exact grant should not cover descendants")
	}
	if aliceReadOnly.Has("engineering/security", Post) || !aliceReadOnly.Has("engineering/security", Read) {
		t.Fatal("credential ceiling must restrict")
	}
}

func TestNestedPrivateAndDeny(t *testing.T) {
	priv := []string{"a", "a/b"}
	p := Policy{Grants: []Grant{
		{ID: "1", Pattern: "a/*", Caps: []string{Read}, Effect: "allow"},
	}, PrivatePaths: priv, Scopes: []string{"*"}, BoardInScope: true}
	if !p.Has("a/c", Read) {
		t.Fatal("a/* should apply inside boundary a")
	}
	if p.Has("a/b", Read) || p.Has("a/b/c", Read) {
		t.Fatal("nested private a/b needs its own allow")
	}
	p.Grants = append(p.Grants, Grant{ID: "2", Pattern: "a/b/*", Caps: []string{Read}, Effect: "allow"})
	if !p.Has("a/b/c", Read) || p.Has("a/b", Read) {
		t.Fatal("a/b/* covers descendants only")
	}
	p.Grants = append(p.Grants, Grant{ID: "3", Pattern: "a/b/c", Caps: []string{Read}, Effect: "deny"})
	if p.Has("a/b/c", Read) {
		t.Fatal("deny must override allow")
	}
	if !p.Has("a/b/d", Read) {
		t.Fatal("deny is exact")
	}
}

func TestBoardAdmin(t *testing.T) {
	p := Policy{IsOwner: true, Scopes: []string{"*"}, BoardInScope: true}
	if !p.IsBoardAdmin() {
		t.Fatal("owner is admin")
	}
	if p.Has("x", Read) {
		t.Fatal("admin role grants no content access")
	}
	p.Grants = []Grant{{ID: "d", Pattern: "*", Caps: []string{BoardAdmin}, Effect: "deny"}}
	if p.IsBoardAdmin() {
		t.Fatal("deny overrides ownership")
	}
	q := Policy{IsOwner: true, Scopes: []string{Read}, BoardInScope: true}
	if q.IsBoardAdmin() {
		t.Fatal("credential without board_admin scope cannot administer")
	}
}

func TestFingerprintStable(t *testing.T) {
	a := Policy{Grants: []Grant{{ID: "1", Pattern: "*", Caps: []string{Post, Read}, Effect: "allow"}}, Scopes: []string{"*"}, BoardInScope: true}
	b := Policy{Grants: []Grant{{ID: "1", Pattern: "*", Caps: []string{Read, Post}, Effect: "allow"}}, Scopes: []string{"*"}, BoardInScope: true}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("fingerprint must be order independent")
	}
	b.PrivatePaths = []string{"x"}
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("private boundary change must change fingerprint")
	}
}
