package conformance

import (
	"fmt"
	"strings"
	"testing"
)

// A regression may return an issued token instead of a problem. Keep the
// response body out of failure output in that case.
func credentialProblem(t *testing.T, status int, code string, r resp) {
	t.Helper()
	if r.Status != status || r.code() != code || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("want %d %s problem, got status %d, code %q", status, code, r.Status, r.code())
	}
}

// CT-DLG-18: the restriction pair ships independently of delegation.
func TestCredentialRestrictions(t *testing.T) {
	u := newActor(t, "restrictions")
	for _, flags := range []struct{ manage, delegate bool }{{true, true}, {true, false}, {false, true}, {false, false}} {
		t.Run(fmt.Sprintf("manage_%t_delegate_%t", flags.manage, flags.delegate), func(t *testing.T) {
			u := &actorClient{t: t, token: u.token, id: u.id}
			issue := func(by *actorClient, body J) (*actorClient, J) {
				t.Helper()
				r := by.must(201, by.do("POST", "/v1/credentials", body, newKey())).Body
				return &actorClient{t: t, token: r["token"].(string), id: u.id}, r["credential"].(J)
			}
			c, view := issue(u, J{"scopes": []string{"*"}, "may_manage_credentials": flags.manage, "may_delegate": flags.delegate})
			check := func(v J) {
				t.Helper()
				if v["may_manage_credentials"] != flags.manage || v["may_delegate"] != flags.delegate {
					t.Fatalf("credential flags = %v, %v; want %v, %v", v["may_manage_credentials"], v["may_delegate"], flags.manage, flags.delegate)
				}
			}
			check(view)
			check(c.must(200, c.do("GET", "/v1/me", nil)).Body["credential"].(J))
			if !flags.manage {
				credentialProblem(t, 403, "credential_restricted", c.do("POST", "/v1/credentials", J{"scopes": []string{"*"}}, newKey()))
				credentialProblem(t, 403, "credential_restricted", c.do("GET", "/v1/credentials", nil))
				other := u.must(200, u.do("GET", "/v1/me", nil)).Body["credential"].(J)
				credentialProblem(t, 403, "credential_restricted", c.do("DELETE", "/v1/credentials/"+other["id"].(string), nil))
				u.must(200, u.do("GET", "/v1/me", nil))
				// Deny before resolving a target.
				credentialProblem(t, 403, "credential_restricted", c.do("DELETE", "/v1/credentials/cred_absent", nil))
			} else {
				child, childView := issue(c, J{"scopes": []string{"read"}})
				check(childView)
				check(child.must(200, child.do("GET", "/v1/me", nil)).Body["credential"].(J))
				list := c.must(200, c.do("GET", "/v1/credentials", nil)).Body["credentials"].([]any)
				found := false
				for _, item := range list {
					v := item.(J)
					if v["id"] == childView["id"] {
						check(v)
						found = true
					}
				}
				if !found {
					t.Fatal("child missing from credential list")
				}
				if !flags.delegate {
					credentialProblem(t, 403, "credential_restricted", c.do("POST", "/v1/credentials", J{"scopes": []string{"read"}, "may_delegate": true}, newKey()))
					credentialProblem(t, 403, "credential_restricted", child.do("POST", "/v1/credentials", J{"scopes": []string{"read"}, "may_delegate": true}, newKey()))
				}
				check(c.must(200, c.do("DELETE", "/v1/credentials/"+childView["id"].(string), nil)).Body)
				credentialProblem(t, 401, "unauthenticated", child.do("GET", "/v1/me", nil))
			}
			check(c.must(200, c.do("DELETE", "/v1/credentials/"+view["id"].(string), nil)).Body)
			credentialProblem(t, 401, "unauthenticated", c.do("GET", "/v1/me", nil))
		})
	}
}

func TestCredentialRestrictionDefaultsAndAdminCeiling(t *testing.T) {
	a := admin(t)
	discovery := a.must(200, a.do("GET", "/v1/server", nil)).Body
	found := false
	for _, capability := range discovery["capabilities"].([]any) {
		if capability == "yak.credentials.restrictions" {
			found = true
		}
	}
	if !found {
		t.Fatal("credential restriction capability missing")
	}
	me := a.must(200, a.do("GET", "/v1/me", nil)).Body["credential"].(J)
	if me["may_manage_credentials"] != true || me["may_delegate"] != true {
		t.Fatal("bootstrap/legacy flags must default to true")
	}
	for _, manage := range []bool{true, false} {
		r := a.must(201, a.do("POST", "/v1/credentials", J{"scopes": []string{"*"}, "may_manage_credentials": manage, "may_delegate": false}, newKey())).Body
		c := &actorClient{t: t, token: r["token"].(string)}
		other := newActor(t, "adminceiling")
		if !manage {
			credentialProblem(t, 403, "credential_restricted", c.do("POST", "/v1/credentials", J{"actor_id": other.id, "scopes": []string{"*"}, "may_manage_credentials": true}, newKey()))
			credentialProblem(t, 403, "credential_restricted", c.do("GET", "/v1/credentials?actor_id="+other.id, nil))
			otherMe := other.must(200, other.do("GET", "/v1/me", nil)).Body["credential"].(J)
			credentialProblem(t, 403, "credential_restricted", c.do("DELETE", "/v1/credentials/"+otherMe["id"].(string), nil))
			other.must(200, other.do("GET", "/v1/me", nil))
			continue
		}
		credentialProblem(t, 403, "credential_restricted", c.do("POST", "/v1/credentials", J{"actor_id": other.id, "scopes": []string{"*"}, "may_delegate": true}, newKey()))
		key := newKey()
		body := J{"actor_id": other.id, "scopes": []string{"*"}}
		issued := c.must(201, c.do("POST", "/v1/credentials", body, key)).Body
		replay := c.must(201, c.do("POST", "/v1/credentials", body, key)).Body
		for _, v := range []J{issued["credential"].(J), replay["credential"].(J)} {
			if v["may_manage_credentials"] != true || v["may_delegate"] != false {
				t.Fatal("cross-actor child did not inherit issuer flags")
			}
		}
		if replay["token"] != nil || replay["token_unavailable"] != true {
			t.Fatal("replay must not expose a token")
		}
	}
}
