package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Personal access tokens let scripts and external CI systems use the API and
// git as a person (or a bot account made for the purpose). They carry the
// user's full permissions; a token is shown once and only its hash is kept.

const patPrefix = "orbit_pat_"

// newSecret returns a random URL-safe secret with the given prefix.
func newSecret(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// APIToken is a token as listed; the secret itself is never shown again.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (s *Server) userByAPIToken(ctx context.Context, token string) (User, error) {
	var u User
	err := s.DB.QueryRow(ctx, `
		update api_tokens t set last_used_at = now()
		  from users u
		 where t.token_hash = $1 and u.id = t.user_id and u.kind = 'human' and (t.expires_at is null or t.expires_at > now())
		returning u.id, u.username, u.display_name, u.is_admin`, hashToken(token)).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin)
	return u, err
}

// userByToken accepts a session token or a personal access token.
func (s *Server) userByToken(ctx context.Context, token string) (User, error) {
	if strings.HasPrefix(token, patPrefix) {
		return s.userByAPIToken(ctx, token)
	}
	return s.userBySession(ctx, token)
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `select id, name, created_at, expires_at, last_used_at from api_tokens where user_id = $1 order by created_at`, currentUser(r).ID)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[APIToken])
	if err != nil {
		return err
	}
	if out == nil {
		out = []APIToken{}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name      string `json:"name"`
		ExpiresIn int    `json:"expires_in_days"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return badRequest("name is required")
	}
	var expires *time.Time
	if in.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(in.ExpiresIn) * 24 * time.Hour)
		expires = &t
	}
	secret, err := newSecret(patPrefix)
	if err != nil {
		return err
	}
	var t APIToken
	err = s.DB.QueryRow(r.Context(), `insert into api_tokens (user_id, name, token_hash, expires_at) values ($1, $2, $3, $4)
		returning id, name, created_at, expires_at, last_used_at`, currentUser(r).ID, in.Name, hashToken(secret), expires).
		Scan(&t.ID, &t.Name, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret})
	return nil
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) error {
	tag, err := s.DB.Exec(r.Context(), `delete from api_tokens where id = $1 and user_id = $2`, chi.URLParam(r, "tokenID"), currentUser(r).ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
