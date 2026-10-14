package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
)

// Webhooks tell other systems what happened in an application: pushes (so
// an external CI can start a build), check updates and finished pipelines.
// Each payload is signed with the hook's secret:
//
//	X-Orbit-Signature: sha256=<hex HMAC-SHA256 of the body>

var webhookEvents = []string{"push", "check", "pipeline"}

// Webhook is a hook as listed. The secret is only shown when it is created.
type Webhook struct {
	ID           string     `json:"id"`
	AppKey       string     `json:"app_key"`
	Repo         *string    `json:"repo"`
	URL          string     `json:"url"`
	Events       []string   `json:"events"`
	Active       bool       `json:"active"`
	CreatedAt    time.Time  `json:"created_at"`
	LastStatus   *int       `json:"last_status"`
	LastError    *string    `json:"last_error"`
	LastDelivery *time.Time `json:"last_delivery_at"`

	secret string
}

// Delivery is one attempt to deliver an event.
type Delivery struct {
	ID         string    `json:"id"`
	Event      string    `json:"event"`
	StatusCode int       `json:"status_code"`
	Error      string    `json:"error"`
	DurationMS int       `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

const webhookCols = `h.id, h.app_key, h.repo_name, h.url, h.events, h.active, h.created_at, h.secret,
	d.status_code, d.error, d.created_at`

const webhookFrom = ` from webhooks h left join lateral (
	select status_code, error, created_at from webhook_deliveries where webhook_id = h.id order by created_at desc limit 1
) d on true`

func scanWebhooks(rows pgx.Rows) ([]Webhook, error) {
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Webhook, error) {
		var h Webhook
		err := row.Scan(&h.ID, &h.AppKey, &h.Repo, &h.URL, &h.Events, &h.Active, &h.CreatedAt, &h.secret, &h.LastStatus, &h.LastError, &h.LastDelivery)
		return h, err
	})
	if out == nil {
		out = []Webhook{}
	}
	return out, err
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	if _, err := s.requireRole(r, app, access.Maintainer); err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `select `+webhookCols+webhookFrom+` where h.app_key = $1 order by h.created_at`, app)
	if err != nil {
		return err
	}
	hooks, err := scanWebhooks(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, hooks)
	return nil
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	if _, err := s.requireRole(r, app, access.Maintainer); err != nil {
		return err
	}
	var in struct {
		URL    string   `json:"url"`
		Repo   *string  `json:"repo"`
		Events []string `json:"events"`
		Secret string   `json:"secret"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return badRequest("url must be an http or https URL")
	}
	if len(in.Events) == 0 {
		in.Events = webhookEvents
	}
	for _, e := range in.Events {
		if !slices.Contains(webhookEvents, e) {
			return badRequest("events must be among " + strings.Join(webhookEvents, ", "))
		}
	}
	if in.Repo != nil && *in.Repo == "" {
		in.Repo = nil
	}
	if in.Repo != nil {
		var exists bool
		if err := s.DB.QueryRow(r.Context(), `select exists (select 1 from repositories where app_key = $1 and name = $2)`, app, *in.Repo).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return badRequest("repository not found")
		}
	}
	if in.Secret == "" {
		if in.Secret, err = newSecret("whsec_"); err != nil {
			return err
		}
	}
	var id string
	if err := s.DB.QueryRow(r.Context(), `insert into webhooks (app_key, repo_name, url, secret, events, created_by) values ($1, $2, $3, $4, $5, $6) returning id`,
		app, in.Repo, u.String(), in.Secret, in.Events, currentUser(r).ID).Scan(&id); err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `select `+webhookCols+webhookFrom+` where h.id = $1`, id)
	if err != nil {
		return err
	}
	hooks, err := scanWebhooks(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": hooks[0], "secret": in.Secret})
	return nil
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	if _, err := s.requireRole(r, app, access.Maintainer); err != nil {
		return err
	}
	tag, err := s.DB.Exec(r.Context(), `delete from webhooks where id = $1 and app_key = $2`, chi.URLParam(r, "hookID"), app)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	if _, err := s.requireRole(r, app, access.Maintainer); err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `
		select d.id, d.event, d.status_code, d.error, d.duration_ms, d.created_at
		  from webhook_deliveries d join webhooks h on h.id = d.webhook_id
		 where h.id = $1 and h.app_key = $2 order by d.created_at desc limit 50`, chi.URLParam(r, "hookID"), app)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Delivery])
	if err != nil {
		return err
	}
	if out == nil {
		out = []Delivery{}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// deliverWebhooks sends an event to the repository's application's hooks in
// the background. Failures are recorded on the hook, not retried.
func (s *Server) deliverWebhooks(ctx context.Context, repo Repo, event string, payload map[string]any) {
	rows, err := s.DB.Query(ctx, `select `+webhookCols+webhookFrom+`
		where h.app_key = $1 and (h.repo_name is null or h.repo_name = $2) and h.active and $3 = any(h.events)`, repo.AppKey, repo.Name, event)
	if err != nil {
		slog.Error("load webhooks", "err", err)
		return
	}
	hooks, err := scanWebhooks(rows)
	if err != nil || len(hooks) == 0 {
		return
	}
	payload["event"] = event
	payload["repository"] = map[string]any{
		"app": repo.AppKey, "name": repo.Name, "default_branch": repo.DefaultBranch,
		"clone_url": s.BaseURL + "/git/" + repo.AppKey + "/" + repo.Name + ".git",
		"url":       s.BaseURL + "/apps/" + repo.AppKey + "/repos/" + repo.Name,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("encode webhook payload", "err", err)
		return
	}
	for _, h := range hooks {
		s.background.Add(1)
		go func() {
			defer s.background.Done()
			s.deliver(h, event, body)
		}()
	}
}

func (s *Server) deliver(h Webhook, event string, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	req, err := http.NewRequestWithContext(ctx, "POST", h.URL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Orbit-Webhook")
	req.Header.Set("X-Orbit-Event", event)
	req.Header.Set("X-Orbit-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	start := time.Now()
	status, errText := 0, ""
	res, err := s.webhookClient.Do(req)
	if err != nil {
		errText = truncate(err.Error(), 500)
	} else {
		status = res.StatusCode
		res.Body.Close()
		if status >= 300 {
			errText = fmt.Sprintf("HTTP %d", status)
		}
	}
	if _, err := s.DB.Exec(context.Background(), `insert into webhook_deliveries (webhook_id, event, status_code, error, duration_ms) values ($1, $2, $3, $4, $5)`,
		h.ID, event, status, errText, time.Since(start).Milliseconds()); err != nil {
		slog.Error("record webhook delivery", "err", err)
	}
}
