package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
)

// A proposal is a write an agent drafted: an issue, a to-do or a comment.
// It shows under the agent's reply with a confirm button for the person who
// asked. Confirming runs the write through the public API as that person, so
// it can never do more than they could themselves.

// ProposalInput is what an agent drafts.
type ProposalInput struct {
	App    string `json:"app,omitempty"`
	Number int    `json:"number,omitempty"` // issue number, for comments
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
}

// ProposalView is a proposal as shown under the reply.
type ProposalView struct {
	ID        string        `json:"id"`
	RunID     string        `json:"run_id"`
	Kind      string        `json:"kind"` // "issue", "todo" or "issue_comment"
	Status    string        `json:"status"`
	Summary   string        `json:"summary"`
	Input     ProposalInput `json:"input"`
	AskerID   *string       `json:"asker_id"`
	Result    *Entity       `json:"result"`
	CreatedAt time.Time     `json:"created_at"`
}

func proposalSummary(kind string, in ProposalInput) string {
	switch kind {
	case "issue":
		return fmt.Sprintf("Create issue in %s: %s", in.App, in.Title)
	case "todo":
		return "Add to-do: " + in.Title
	case "issue_comment":
		return fmt.Sprintf("Comment on %s#%d", in.App, in.Number)
	}
	return kind
}

func (s *Server) loadProposals(ctx context.Context, run RunView) ([]ProposalView, error) {
	rows, err := s.DB.Query(ctx, `select id, run_id, kind, status, input, result, created_at from agent_proposals where run_id = $1 order by created_at`, run.ID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProposalView, error) {
		var p ProposalView
		err := row.Scan(&p.ID, &p.RunID, &p.Kind, &p.Status, &p.Input, &p.Result, &p.CreatedAt)
		p.AskerID = run.AskerID
		p.Summary = proposalSummary(p.Kind, p.Input)
		return p, err
	})
	if out == nil {
		out = []ProposalView{}
	}
	return out, err
}

func (s *Server) createProposal(ctx context.Context, runID, kind string, in ProposalInput) (ProposalView, error) {
	p := ProposalView{RunID: runID, Kind: kind, Status: "pending", Input: in, Summary: proposalSummary(kind, in)}
	err := s.DB.QueryRow(ctx, `insert into agent_proposals (run_id, kind, input) values ($1, $2, $3) returning id, created_at`,
		runID, kind, in).Scan(&p.ID, &p.CreatedAt)
	return p, err
}

// claimProposal moves a pending proposal of the current user's run to
// "applying" (or "dismissed"), so a double click cannot apply it twice.
func (s *Server) claimProposal(r *http.Request, to string) (ProposalView, RunView, error) {
	ctx := r.Context()
	id := chi.URLParam(r, "proposalID")
	var runID string
	if err := s.DB.QueryRow(ctx, `select run_id from agent_proposals where id = $1`, id).Scan(&runID); err != nil {
		return ProposalView{}, RunView{}, err
	}
	run, err := s.visibleRun(r, runID)
	if err != nil {
		return ProposalView{}, run, err
	}
	if run.AskerID == nil || *run.AskerID != currentUser(r).ID {
		return ProposalView{}, run, errStatus(http.StatusForbidden, "only the person who asked can confirm or dismiss this")
	}
	var p ProposalView
	err = s.DB.QueryRow(ctx, `update agent_proposals set status = $2, decided_at = now() where id = $1 and status = 'pending'
		returning id, run_id, kind, input`, id, to).Scan(&p.ID, &p.RunID, &p.Kind, &p.Input)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, run, errStatus(http.StatusConflict, "this was already confirmed or dismissed")
	}
	return p, run, err
}

func (s *Server) applyProposal(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, run, err := s.claimProposal(r, "applying")
	if err != nil {
		return err
	}
	result, err := s.performProposal(r, p, run)
	if err != nil {
		if _, rerr := s.DB.Exec(context.WithoutCancel(ctx), `update agent_proposals set status = 'pending', decided_at = null where id = $1`, p.ID); rerr != nil {
			return rerr
		}
		var ae *apiCallError
		if errors.As(err, &ae) {
			return errStatus(ae.Status, ae.Msg)
		}
		return err
	}
	if _, err := s.DB.Exec(ctx, `update agent_proposals set status = 'applied', result = $2 where id = $1`, p.ID, result); err != nil {
		return err
	}
	if err := addLink(ctx, s.DB, result.Ref, Ref{Type: "agent_run", ID: run.ID}, LinkProposedBy, nil, currentUser(r).ID); err != nil {
		return err
	}
	if err := s.refreshReply(ctx, run.ID, "message.updated"); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
	return nil
}

func (s *Server) dismissProposal(w http.ResponseWriter, r *http.Request) error {
	_, run, err := s.claimProposal(r, "dismissed")
	if err != nil {
		return err
	}
	if err := s.refreshReply(r.Context(), run.ID, "message.updated"); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// performProposal makes the write as the current user, through the API.
// The agent's own limits are checked again: its definition may have changed
// since it drafted the proposal.
func (s *Server) performProposal(r *http.Request, p ProposalView, run RunView) (*Entity, error) {
	ctx := r.Context()
	u := currentUser(r)
	if s.agents == nil {
		return nil, errStatus(http.StatusConflict, "agents are not enabled on this instance")
	}
	def, ok := s.agents.defs[run.Agent]
	if !ok {
		return nil, errStatus(http.StatusConflict, "@"+run.Agent+" is no longer available")
	}
	if p.Input.App != "" {
		role, err := s.Access.AppRole(ctx, u.ID, p.Input.App)
		if err != nil {
			return nil, err
		}
		if !def.AllowsApp(p.Input.App) || access.Min(role, access.ParseRole(def.Role)) < access.Reader {
			return nil, errForbidden
		}
	}
	var source *Ref
	if run.TriggerID != nil {
		source = &Ref{Type: "message", ID: *run.TriggerID}
	}

	switch p.Kind {
	case "issue":
		var out struct {
			Issue Issue `json:"issue"`
		}
		in := map[string]any{"title": p.Input.Title, "body": p.Input.Body}
		if source != nil {
			in["source"] = source
		}
		if err := s.callAPI(ctx, u, "POST", "/apps/"+url.PathEscape(p.Input.App)+"/issues", in, &out); err != nil {
			return nil, err
		}
		e, err := s.resolve(ctx, Ref{Type: "issue", ID: out.Issue.ID})
		return &e, err
	case "todo":
		var out Todo
		in := map[string]any{"title": p.Input.Title}
		if source != nil {
			in["source"] = source
		}
		if err := s.callAPI(ctx, u, "POST", "/todos", in, &out); err != nil {
			return nil, err
		}
		e, err := s.resolve(ctx, Ref{Type: "todo", ID: out.ID})
		return &e, err
	case "issue_comment":
		var issue struct {
			Issue Issue `json:"issue"`
		}
		if err := s.callAPI(ctx, u, "GET", fmt.Sprintf("/apps/%s/issues/%d", url.PathEscape(p.Input.App), p.Input.Number), nil, &issue); err != nil {
			return nil, err
		}
		var m Message
		if err := s.callAPI(ctx, u, "POST", "/channels/"+issue.Issue.ChannelID+"/messages", map[string]any{"body": p.Input.Body}, &m); err != nil {
			return nil, err
		}
		e, err := s.resolve(ctx, Ref{Type: "message", ID: m.ID})
		return &e, err
	}
	return nil, fmt.Errorf("unknown proposal kind %q", p.Kind)
}

// proposalJSON is what a propose tool returns to the model.
func proposalJSON(p ProposalView) (string, error) {
	out, err := json.Marshal(map[string]any{
		"proposal": map[string]any{"id": p.ID, "kind": p.Kind, "summary": p.Summary},
		"note":     "Shown to the person with a confirm button. Nothing is created until they confirm; do not claim it was created.",
	})
	return string(out), err
}
