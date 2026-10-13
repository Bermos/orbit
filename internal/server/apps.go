package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
)

// App is an application: the stable root of the resource tree.
type App struct {
	Key         string    `json:"key"`  // short name: URL slug and ID, never changes
	Name        string    `json:"name"` // long name, shown in the UI
	Description string    `json:"description"`
	OwnerTeam   TeamRef   `json:"owner_team"`
	CreatedAt   time.Time `json:"created_at"`
}

type TeamRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const appCols = `a.key, a.name, a.description, ot.id, ot.name, a.created_at`

func scanApps(rows pgx.Rows) ([]App, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (App, error) {
		var a App
		err := row.Scan(&a.Key, &a.Name, &a.Description, &a.OwnerTeam.ID, &a.OwnerTeam.Name, &a.CreatedAt)
		return a, err
	})
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `select `+appCols+` from applications a join teams ot on ot.id = a.owner_team_id order by a.name`)
	if err != nil {
		return err
	}
	apps, err := scanApps(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apps)
	return nil
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
		OwnerTeamID string `json:"owner_team_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Key = strings.ToLower(strings.TrimSpace(in.Key))
	if strings.TrimSpace(in.Name) == "" {
		return badRequest("name is required")
	}
	u := currentUser(r)
	ctx := r.Context()
	if !u.IsAdmin {
		var member bool
		if err := s.DB.QueryRow(ctx, `select exists (select 1 from team_members where team_id = $1 and user_id = $2)`,
			in.OwnerTeamID, u.ID).Scan(&member); err != nil {
			return err
		}
		if !member {
			return errStatus(http.StatusForbidden, "you can only create applications owned by a team you are in")
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `insert into applications (key, name, description, owner_team_id) values ($1, $2, $3, $4)`,
		in.Key, strings.TrimSpace(in.Name), in.Description, in.OwnerTeamID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into channels (app_key, kind, name) values ($1, 'chat', 'general')`, in.Key); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.writeApp(w, r, in.Key, http.StatusCreated)
}

type AppTeam struct {
	TeamRef
	Role string `json:"role"`
}

func (s *Server) writeApp(w http.ResponseWriter, r *http.Request, key string, status int) error {
	ctx := r.Context()
	rows, err := s.DB.Query(ctx, `select `+appCols+` from applications a join teams ot on ot.id = a.owner_team_id where a.key = $1`, key)
	if err != nil {
		return err
	}
	apps, err := scanApps(rows)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return errNotFound
	}
	role, err := s.Access.AppRole(ctx, currentUser(r).ID, key)
	if err != nil {
		return err
	}

	rows, err = s.DB.Query(ctx, `select t.id, t.name, at.role from application_teams at join teams t on t.id = at.team_id where at.app_key = $1 order by t.name`, key)
	if err != nil {
		return err
	}
	teams, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AppTeam, error) {
		var t AppTeam
		err := row.Scan(&t.ID, &t.Name, &t.Role)
		return t, err
	})
	if err != nil {
		return err
	}

	rows, err = s.DB.Query(ctx, `select `+repoCols+` from repositories r where r.app_key = $1 order by r.name`, key)
	if err != nil {
		return err
	}
	repos, err := scanRepos(rows)
	if err != nil {
		return err
	}

	rows, err = s.DB.Query(ctx, `select `+channelCols+` from channels c where c.app_key = $1 and c.kind = 'chat' order by c.name`, key)
	if err != nil {
		return err
	}
	channels, err := scanChannels(rows)
	if err != nil {
		return err
	}

	writeJSON(w, status, map[string]any{
		"application":       apps[0],
		"responsible_teams": teams,
		"repositories":      repos,
		"channels":          channels,
		"my_role":           role.String(),
	})
	return nil
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) error {
	if _, err := s.requireRole(r, chi.URLParam(r, "app"), access.Reader); err != nil {
		return err
	}
	return s.writeApp(w, r, chi.URLParam(r, "app"), http.StatusOK)
}

// updateApp changes the long name, description or owning team. The key is
// immutable by design.
func (s *Server) updateApp(w http.ResponseWriter, r *http.Request) error {
	key := chi.URLParam(r, "app")
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		OwnerTeamID *string `json:"owner_team_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if _, err := s.requireRole(r, key, access.Maintainer); err != nil {
		return err
	}
	ctx := r.Context()
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return badRequest("name is required")
		}
		if _, err := s.DB.Exec(ctx, `update applications set name = $2 where key = $1`, key, strings.TrimSpace(*in.Name)); err != nil {
			return err
		}
	}
	if in.Description != nil {
		if _, err := s.DB.Exec(ctx, `update applications set description = $2 where key = $1`, key, *in.Description); err != nil {
			return err
		}
	}
	if in.OwnerTeamID != nil {
		if _, err := s.DB.Exec(ctx, `update applications set owner_team_id = $2 where key = $1`, key, *in.OwnerTeamID); err != nil {
			return err
		}
	}
	return s.writeApp(w, r, key, http.StatusOK)
}

func (s *Server) putAppTeam(w http.ResponseWriter, r *http.Request) error {
	key, teamID := chi.URLParam(r, "app"), chi.URLParam(r, "teamID")
	var in struct {
		Role string `json:"role"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Role == "" {
		in.Role = "developer"
	}
	if _, err := s.requireRole(r, key, access.Maintainer); err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `
		insert into application_teams (app_key, team_id, role) values ($1, $2, $3)
		on conflict (app_key, team_id) do update set role = excluded.role`, key, teamID, in.Role); err != nil {
		return err
	}
	return s.writeApp(w, r, key, http.StatusOK)
}

func (s *Server) deleteAppTeam(w http.ResponseWriter, r *http.Request) error {
	key, teamID := chi.URLParam(r, "app"), chi.URLParam(r, "teamID")
	if _, err := s.requireRole(r, key, access.Maintainer); err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `delete from application_teams where app_key = $1 and team_id = $2`, key, teamID); err != nil {
		return err
	}
	return s.writeApp(w, r, key, http.StatusOK)
}
