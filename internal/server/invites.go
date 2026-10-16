package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Invites are how people join an instance whose registration is
// invite-only: an admin creates a single-use link and hands it out. Only the
// hash of the invite's secret is kept, like tokens.

const invitePrefix = "orbit_inv_"

// Invite is an invite as listed.
type Invite struct {
	ID        string     `json:"id"`
	Note      string     `json:"note"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
	UsedBy    *string    `json:"used_by"`
}

func requireAdmin(r *http.Request) error {
	if !currentUser(r).IsAdmin {
		return errStatus(http.StatusForbidden, "only instance admins manage invites")
	}
	return nil
}

func (s *Server) listInvites(w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `
		select i.id, i.note, i.created_at, i.expires_at, i.used_at, u.username
		  from invites i left join users u on u.id = i.used_by
		 order by i.created_at desc limit 100`)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Invite])
	if err != nil {
		return err
	}
	if out == nil {
		out = []Invite{}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	var in struct {
		Note      string `json:"note"`
		ExpiresIn int    `json:"expires_in_days"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.ExpiresIn <= 0 {
		in.ExpiresIn = 7
	}
	if in.ExpiresIn > 90 {
		return badRequest("invites expire after at most 90 days")
	}
	secret, err := newSecret(invitePrefix)
	if err != nil {
		return err
	}
	var inv Invite
	err = s.DB.QueryRow(r.Context(), `
		insert into invites (token_hash, note, created_by, expires_at) values ($1, $2, $3, $4)
		returning id, note, created_at, expires_at, used_at, null::text`,
		hashToken(secret), strings.TrimSpace(in.Note), currentUser(r).ID, time.Now().Add(time.Duration(in.ExpiresIn)*24*time.Hour)).
		Scan(&inv.ID, &inv.Note, &inv.CreatedAt, &inv.ExpiresAt, &inv.UsedAt, &inv.UsedBy)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"invite": inv,
		"secret": secret,
		"url":    s.BaseURL + "/login?invite=" + secret,
	})
	return nil
}

func (s *Server) deleteInvite(w http.ResponseWriter, r *http.Request) error {
	if err := requireAdmin(r); err != nil {
		return err
	}
	tag, err := s.DB.Exec(r.Context(), `delete from invites where id = $1`, chi.URLParam(r, "inviteID"))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
