package httpapi

// P2 task routes (ADR-0016 §2). Authorization is in the domain layer.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/abby-protocol/abby/internal/domain"
	"github.com/abby-protocol/abby/internal/events"
)

// claimProof reads Abby-Claim-Proof. The proof is accepted only as a header
// (CLAIM-012); a missing header on a claimant route is 428 (CLAIM-013).
func claimProof(r *http.Request, required bool) (*domain.ClaimProof, error) {
	q := r.URL.Query()
	if q.Has("proof") || q.Has("claim_proof") {
		return nil, domain.Validation("the claim proof is accepted only in the Abby-Claim-Proof header")
	}
	h := r.Header.Get("Abby-Claim-Proof")
	if h == "" {
		if !required {
			return nil, nil
		}
		e := domain.E(428, "precondition_required", "Abby-Claim-Proof header is required for claimant operations")
		e.Extra = map[string]any{"header": "Abby-Claim-Proof"}
		return nil, e
	}
	return domain.ParseClaimProof(h)
}

// optionalIdem: PUT assignee/dependencies MAY carry Idempotency-Key.
func optionalIdem(r *http.Request, ns string) (*domain.Idem, error) {
	if r.Header.Get("Idempotency-Key") == "" {
		return nil, nil
	}
	return idem(r, ns)
}

// respondTask writes a mutation result and the task ETag when the body is a
// task view.
func (a *API) respondTask(w http.ResponseWriter, r *http.Request, p *domain.Principal, boardID string, res *domain.Result) error {
	if !res.Replayed {
		if t, ok := res.Body.(domain.TaskView); ok {
			etag(w, t.Revision)
		}
	}
	return a.respond(w, r, p, boardID, res)
}

func (a *API) taskRoutes() {
	m := a.mux
	const T = "/v1/boards/{board_id}/tasks"
	bid := func(r *http.Request) string { return r.PathValue("board_id") }
	tid := func(r *http.Request) string { return r.PathValue("task_id") }

	m.HandleFunc("GET "+T, a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		q := r.URL.Query()
		for _, bad := range []string{"mention", "text", "type", "author_id", "thread_id"} {
			if q.Has(bad) {
				return domain.Validation("task listing supports channel, subtree, state, assignee, owner and label filters")
			}
		}
		lf, err := events.ParseFilter(q)
		if err != nil {
			return domain.Validation("%s", err)
		}
		after, limit, err := pageArgs(r, a.S)
		if err != nil {
			return err
		}
		f := domain.TaskFilter{Channel: lf.Channel, Subtree: lf.Subtree, States: q["state"], Assignee: q.Get("assignee"), Owner: q.Get("owner"), Labels: lf.Labels}
		pg, err := a.S.ListTasks(r.Context(), p, bid(r), f, after, limit)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, pg)
	}))
	m.HandleFunc("POST "+T, a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.CreateTaskRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.CreateTask(r.Context(), p, bid(r), i, req)
		if err != nil {
			return err
		}
		return a.respondTask(w, r, p, bid(r), res)
	}))
	m.HandleFunc("POST "+T+"/batch", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.CreateTaskBatchRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.CreateTaskBatch(r.Context(), p, bid(r), i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, bid(r), res)
	}))
	m.HandleFunc("POST "+T+"/claim-next", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.ClaimNextRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		res, nw, err := a.S.ClaimNext(r.Context(), p, tok, bid(r), i, req)
		if err != nil {
			return err
		}
		if nw != nil {
			return writeJSON(w, 200, nw)
		}
		return a.respond(w, r, p, bid(r), res)
	}))
	m.HandleFunc("GET "+T+"/{task_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		t, err := a.S.GetTask(r.Context(), p, bid(r), tid(r))
		if err != nil {
			return err
		}
		etag(w, t.Revision)
		return writeJSON(w, 200, t)
	}))
	m.HandleFunc("PATCH "+T+"/{task_id}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.PatchTaskRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		t, err := a.S.PatchTask(r.Context(), p, bid(r), tid(r), *im, req)
		if err != nil {
			return err
		}
		etag(w, t.Revision)
		return writeJSON(w, 200, t)
	}))

	// briefs
	m.HandleFunc("GET "+T+"/{task_id}/briefs", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		after, limit, err := pageArgs(r, a.S)
		if err != nil {
			return err
		}
		pg, err := a.S.ListBriefs(r.Context(), p, bid(r), tid(r), after, limit)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, pg)
	}))
	m.HandleFunc("GET "+T+"/{task_id}/briefs/{revision}", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		rev, err := strconv.Atoi(r.PathValue("revision"))
		if err != nil || rev < 1 {
			return domain.NotFound("brief")
		}
		b, err := a.S.GetBrief(r.Context(), p, bid(r), tid(r), rev)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, b)
	}))
	m.HandleFunc("POST "+T+"/{task_id}/briefs", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.BriefUpdateRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.UpdateBrief(r.Context(), p, bid(r), tid(r), *im, i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, bid(r), res)
	}))
	m.HandleFunc("POST "+T+"/{task_id}/root-redaction", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.ReasonRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.RedactRoot(r.Context(), p, bid(r), tid(r), *im, i, req)
		if err != nil {
			return err
		}
		return a.respondTask(w, r, p, bid(r), res)
	}))

	// claims
	m.HandleFunc("GET "+T+"/{task_id}/claims", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		before, limit, err := pageArgs(r, a.S)
		if err != nil {
			return err
		}
		pg, err := a.S.ListClaims(r.Context(), p, bid(r), tid(r), before, limit)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, pg)
	}))
	m.HandleFunc("POST "+T+"/{task_id}/claims", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.ClaimRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.ClaimTask(r.Context(), p, bid(r), tid(r), i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, bid(r), res)
	}))
	m.HandleFunc("POST "+T+"/{task_id}/claims/{claim_id}/renew", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.RenewRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		proof, err := claimProof(r, true)
		if err != nil {
			return err
		}
		i, err := idem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.RenewClaim(r.Context(), p, bid(r), tid(r), r.PathValue("claim_id"), proof, i, req)
		if err != nil {
			return err
		}
		return a.respond(w, r, p, bid(r), res)
	}))

	// claimant commands
	claimant := func(name string, call func(r *http.Request, p *domain.Principal, proof *domain.ClaimProof, i *domain.Idem) (*domain.Result, error)) {
		m.HandleFunc("POST "+T+"/{task_id}/"+name, a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
			proof, err := claimProof(r, true)
			if err != nil {
				return err
			}
			i, err := idem(r, bid(r))
			if err != nil {
				return err
			}
			res, err := call(r, p, proof, i)
			if err != nil {
				return err
			}
			return a.respondTask(w, r, p, bid(r), res)
		}))
	}
	claimant("release", func(r *http.Request, p *domain.Principal, proof *domain.ClaimProof, i *domain.Idem) (*domain.Result, error) {
		var req domain.ReasonRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return a.S.ReleaseTask(r.Context(), p, bid(r), tid(r), proof, i, req)
	})
	claimant("complete", func(r *http.Request, p *domain.Principal, proof *domain.ClaimProof, i *domain.Idem) (*domain.Result, error) {
		var req domain.CompleteRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return a.S.CompleteTask(r.Context(), p, bid(r), tid(r), proof, i, req)
	})
	claimant("fail", func(r *http.Request, p *domain.Principal, proof *domain.ClaimProof, i *domain.Idem) (*domain.Result, error) {
		var req domain.FailRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return a.S.FailTask(r.Context(), p, bid(r), tid(r), proof, i, req)
	})

	// manager lifecycle commands (block also has a claimant mode)
	manager := func(name string, call func(r *http.Request, p *domain.Principal, im *int64, i *domain.Idem, req domain.ReasonRequest) (*domain.Result, error)) {
		m.HandleFunc("POST "+T+"/{task_id}/"+name, a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
			var req domain.ReasonRequest
			if err := decode(r, &req); err != nil {
				return err
			}
			im, err := ifMatch(r, false)
			if err != nil {
				return err
			}
			i, err := idem(r, bid(r))
			if err != nil {
				return err
			}
			res, err := call(r, p, im, i, req)
			if err != nil {
				return err
			}
			return a.respondTask(w, r, p, bid(r), res)
		}))
	}
	manager("block", func(r *http.Request, p *domain.Principal, im *int64, i *domain.Idem, req domain.ReasonRequest) (*domain.Result, error) {
		proof, err := claimProof(r, false)
		if err != nil {
			return nil, err
		}
		return a.S.BlockTask(r.Context(), p, bid(r), tid(r), proof, im, i, req)
	})
	manager("unblock", func(r *http.Request, p *domain.Principal, im *int64, i *domain.Idem, req domain.ReasonRequest) (*domain.Result, error) {
		return a.S.UnblockTask(r.Context(), p, bid(r), tid(r), im, i, req)
	})
	manager("cancel", func(r *http.Request, p *domain.Principal, im *int64, i *domain.Idem, req domain.ReasonRequest) (*domain.Result, error) {
		return a.S.CancelTask(r.Context(), p, bid(r), tid(r), im, i, req)
	})
	manager("reopen", func(r *http.Request, p *domain.Principal, im *int64, i *domain.Idem, req domain.ReasonRequest) (*domain.Result, error) {
		return a.S.ReopenTask(r.Context(), p, bid(r), tid(r), im, i, req)
	})

	m.HandleFunc("PUT "+T+"/{task_id}/assignee", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.AssigneeRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		i, err := optionalIdem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.SetAssignee(r.Context(), p, bid(r), tid(r), *im, i, req)
		if err != nil {
			return err
		}
		return a.respondTask(w, r, p, bid(r), res)
	}))
	m.HandleFunc("PUT "+T+"/{task_id}/dependencies", a.authed(func(w http.ResponseWriter, r *http.Request, p *domain.Principal) error {
		var req domain.DependenciesRequest
		if err := decode(r, &req); err != nil {
			return err
		}
		im, err := ifMatch(r, true)
		if err != nil {
			return err
		}
		i, err := optionalIdem(r, bid(r))
		if err != nil {
			return err
		}
		res, err := a.S.SetDependencies(r.Context(), p, bid(r), tid(r), *im, i, req)
		if err != nil {
			return err
		}
		return a.respondTask(w, r, p, bid(r), res)
	}))
}
