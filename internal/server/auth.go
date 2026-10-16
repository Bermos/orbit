package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Prototype auth: local accounts and opaque session tokens, sent either as a
// cookie (web UI) or as a bearer token (API clients). An OIDC provider can be
// added later behind the same session model.

const (
	sessionCookie = "orbit_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// dummyHash is compared against when a username does not exist.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("orbit-no-such-user"), bcrypt.DefaultCost)

// User is the signed-in user.
type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	IsAdmin     bool   `json:"is_admin"`
}

type ctxKey struct{}

func currentUser(r *http.Request) User {
	u, _ := r.Context().Value(ctxKey{}).(User)
	return u
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Server) userBySession(ctx context.Context, token string) (User, error) {
	var u User
	err := s.DB.QueryRow(ctx, `
		select u.id, u.username, u.display_name, u.is_admin
		  from sessions s join users u on u.id = s.user_id
		 where s.token_hash = $1 and s.expires_at > now() and u.kind = 'human'`, hashToken(token)).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin)
	return u, err
}

// userByPassword checks a username and password. Used by login and by git
// over HTTPS (basic auth).
func (s *Server) userByPassword(ctx context.Context, username, password string) (User, error) {
	var u User
	var hash string
	err := s.DB.QueryRow(ctx, `select id, username, display_name, is_admin, password_hash from users where username = $1 and kind = 'human'`,
		strings.ToLower(username)).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// Spend the same time as a real check so usernames cannot be probed.
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return User{}, errStatus(http.StatusUnauthorized, "invalid username or password")
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, errStatus(http.StatusUnauthorized, "invalid username or password")
	}
	return u, nil
}

func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		} else if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required"})
			return
		}
		u, err := s.userByToken(r.Context(), token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u User) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	expires := time.Now().Add(sessionTTL)
	if _, err := s.DB.Exec(r.Context(), `insert into sessions (token_hash, user_id, expires_at) values ($1, $2, $3)`,
		hashToken(token), u.ID, expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   r.TLS != nil || strings.HasPrefix(s.BaseURL, "https://"),
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "token": token})
	return nil
}

// Who may create an account.
const (
	// RegistrationInvite: only people with an invite from an admin.
	RegistrationInvite = "invite"
	// RegistrationOpen: anyone who can reach the instance.
	RegistrationOpen = "open"
)

// authConfig tells the sign-in page which forms to offer.
func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) error {
	setup, err := s.NeedsSetup(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"registration":         s.Registration,
		"setup_required":       setup,
		"setup_token_required": setup && s.SetupToken != "",
	})
	return nil
}

// NeedsSetup reports whether the instance has no people yet.
func (s *Server) NeedsSetup(ctx context.Context) (bool, error) {
	var none bool
	err := s.DB.QueryRow(ctx, `select not exists (select 1 from users where kind = 'human')`).Scan(&none)
	return none, err
}

// EnsureAdmin creates the instance admin when there are no people yet. It
// never changes an existing account.
func (s *Server) EnsureAdmin(ctx context.Context, username, password string) (bool, error) {
	if err := validPassword(password); err != nil {
		return false, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, err
	}
	username = strings.ToLower(strings.TrimSpace(username))
	tag, err := s.DB.Exec(ctx, `
		insert into users (username, display_name, password_hash, is_admin)
		select $1, $1, $2, true where not exists (select 1 from users where kind = 'human')`, username, string(hash))
	return tag.RowsAffected() == 1, err
}

func validPassword(p string) error {
	if len(p) < 8 {
		return badRequest("password must be at least 8 characters")
	}
	if len(p) > 72 {
		return badRequest("password must be at most 72 bytes")
	}
	return nil
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Invite      string `json:"invite"`
		SetupToken  string `json:"setup_token"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ip := remoteIP(r)
	if err := tooManyAttempts(w, func() time.Duration { return s.limits.register.retryAfter(ip) }); err != nil {
		return err
	}
	s.limits.register.add(ip)
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	if err := validPassword(in.Password); err != nil {
		return err
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		in.DisplayName = in.Username
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// One registration at a time, so two people cannot both become the
	// first account.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('orbit.register'))`); err != nil {
		return err
	}
	var first bool
	if err := tx.QueryRow(ctx, `select not exists (select 1 from users where kind = 'human')`).Scan(&first); err != nil {
		return err
	}
	var inviteID *string
	switch {
	case first:
		// The first account becomes the instance admin.
		if s.SetupToken != "" && subtle.ConstantTimeCompare([]byte(in.SetupToken), []byte(s.SetupToken)) != 1 {
			return errStatus(http.StatusForbidden, "the setup token is missing or wrong; Orbit logs it at startup")
		}
	case in.Invite != "":
		var id string
		err := tx.QueryRow(ctx, `update invites set used_at = now() where token_hash = $1 and used_at is null and expires_at > now() returning id`,
			hashToken(in.Invite)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errStatus(http.StatusForbidden, "this invite is invalid, expired or already used")
		}
		if err != nil {
			return err
		}
		inviteID = &id
	case s.Registration == RegistrationOpen:
	default:
		return errStatus(http.StatusForbidden, "you need an invite to create an account; ask an admin")
	}

	var u User
	err = tx.QueryRow(ctx, `
		insert into users (username, display_name, password_hash, is_admin)
		values ($1, $2, $3, $4)
		returning id, username, display_name, is_admin`,
		in.Username, strings.TrimSpace(in.DisplayName), string(hash), first).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin)
	if err != nil {
		return err
	}
	if inviteID != nil {
		if _, err := tx.Exec(ctx, `update invites set used_by = $1 where id = $2`, u.ID, *inviteID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.startSession(w, r, u)
}

// checkPassword is userByPassword with rate limits on failures, for every
// place a password is accepted.
func (s *Server) checkPassword(w http.ResponseWriter, r *http.Request, username, password string) (User, error) {
	ip, user := remoteIP(r), strings.ToLower(username)
	if err := tooManyAttempts(w,
		func() time.Duration { return s.limits.authIP.retryAfter(ip) },
		func() time.Duration { return s.limits.authUser.retryAfter(user) },
	); err != nil {
		return User{}, err
	}
	u, err := s.userByPassword(r.Context(), username, password)
	var ae *apiError
	if errors.As(err, &ae) && ae.status == http.StatusUnauthorized {
		s.limits.authIP.add(ip)
		s.limits.authUser.add(user)
	} else if err == nil {
		s.limits.authUser.reset(user)
	}
	return u, err
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u, err := s.checkPassword(w, r, in.Username, in.Password)
	if err != nil {
		return err
	}
	return s.startSession(w, r, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.DB.Exec(r.Context(), `delete from sessions where token_hash = $1`, hashToken(c.Value))
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		s.DB.Exec(r.Context(), `delete from sessions where token_hash = $1`, hashToken(strings.TrimPrefix(h, "Bearer ")))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, currentUser(r))
	return nil
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `select id, username, display_name, is_admin from users where kind = 'human' order by display_name`)
	if err != nil {
		return err
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) {
		var u User
		err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin)
		return u, err
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, users)
	return nil
}
