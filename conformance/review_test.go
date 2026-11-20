package conformance

// Regression tests for findings of the 2026-11-19 independent P1 review.

import "testing"

// H3: an administrator's credential is also bounded by its own ceiling.
func TestAdminCredentialCeiling(t *testing.T) { // AUTH-005
	a := admin(t)
	me := a.must(200, a.do("GET", "/v1/me", nil)).Body
	narrow := credential(t, me["actor"].(J)["id"].(string), "server_admin")
	narrow.wantProblem(403, "forbidden", narrow.do("POST", "/v1/credentials", J{"scopes": []string{"*"}}, newKey()))
	// Within the ceiling is fine, and the result cannot outlive the issuer.
	got := narrow.must(201, narrow.do("POST", "/v1/credentials", J{"scopes": []string{"server_admin"}, "expires_in_seconds": 31536000}, newKey())).Body
	nm := narrow.must(200, narrow.do("GET", "/v1/me", nil)).Body
	if got["credential"].(J)["expires_at"].(string) > nm["credential"].(J)["expires_at"].(string) {
		t.Fatal("self-issued credential outlives its issuer")
	}
}

// M4: revoking a credential also disables credentials minted from it.
func TestRevocationCascades(t *testing.T) { // AUTH-004
	u := newActor(t, "casc")
	child := u.must(201, u.do("POST", "/v1/credentials", J{"scopes": []string{"read"}}, newKey())).Body
	c := &actorClient{t: t, token: child["token"].(string), id: u.id}
	c.must(200, c.do("GET", "/v1/me", nil))
	me := u.must(200, u.do("GET", "/v1/me", nil)).Body
	admin(t).must(200, admin(t).do("DELETE", "/v1/credentials/"+me["credential"].(J)["id"].(string), nil))
	c.wantProblem(401, "unauthenticated", c.do("GET", "/v1/me", nil))
	// Credentials an administrator issued to other actors are independent.
	other := newActor(t, "indep")
	other.must(200, other.do("GET", "/v1/me", nil))
}
