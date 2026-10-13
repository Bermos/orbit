package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Team is a node in the org tree.
type Team struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	ParentID    *string `json:"parent_id"`
	MemberCount int     `json:"member_count"`
}

type TeamMember struct {
	User
	Role string `json:"role"`
}

func scanTeams(rows pgx.Rows) ([]Team, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Team, error) {
		var t Team
		err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.ParentID, &t.MemberCount)
		return t, err
	})
}

const teamCols = `t.id, t.slug, t.name, t.parent_id, (select count(*) from team_members m where m.team_id = t.id)`

func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `select `+teamCols+` from teams t order by t.name`)
	if err != nil {
		return err
	}
	teams, err := scanTeams(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, teams)
	return nil
}

func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Slug     string  `json:"slug"`
		Name     string  `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if strings.TrimSpace(in.Name) == "" {
		return badRequest("name is required")
	}
	u := currentUser(r)
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var id string
	if err := tx.QueryRow(r.Context(), `insert into teams (slug, name, parent_id) values ($1, $2, $3) returning id`,
		in.Slug, strings.TrimSpace(in.Name), in.ParentID).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.Exec(r.Context(), `insert into team_members (team_id, user_id, role) values ($1, $2, 'lead')`, id, u.ID); err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	return s.writeTeam(w, r, id, http.StatusCreated)
}

func (s *Server) writeTeam(w http.ResponseWriter, r *http.Request, id string, status int) error {
	ctx := r.Context()
	rows, err := s.DB.Query(ctx, `select `+teamCols+` from teams t where t.id = $1`, id)
	if err != nil {
		return err
	}
	teams, err := scanTeams(rows)
	if err != nil {
		return err
	}
	if len(teams) == 0 {
		return errNotFound
	}

	rows, err = s.DB.Query(ctx, `
		select u.id, u.username, u.display_name, u.is_admin, m.role
		  from team_members m join users u on u.id = m.user_id
		 where m.team_id = $1 order by m.role desc, u.display_name`, id)
	if err != nil {
		return err
	}
	members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TeamMember, error) {
		var m TeamMember
		err := row.Scan(&m.ID, &m.Username, &m.DisplayName, &m.IsAdmin, &m.Role)
		return m, err
	})
	if err != nil {
		return err
	}

	rows, err = s.DB.Query(ctx, `select `+teamCols+` from teams t where t.parent_id = $1 order by t.name`, id)
	if err != nil {
		return err
	}
	children, err := scanTeams(rows)
	if err != nil {
		return err
	}

	// Ancestors, root first, for breadcrumbs.
	rows, err = s.DB.Query(ctx, `
		with recursive up as (
			select id, parent_id, 0 as depth from teams where id = (select parent_id from teams where id = $1)
			union all
			select t.id, t.parent_id, up.depth + 1 from teams t join up on t.id = up.parent_id
		)
		select `+teamCols+` from up join teams t on t.id = up.id order by up.depth desc`, id)
	if err != nil {
		return err
	}
	ancestors, err := scanTeams(rows)
	if err != nil {
		return err
	}

	writeJSON(w, status, map[string]any{
		"team":      teams[0],
		"members":   members,
		"children":  children,
		"ancestors": ancestors,
	})
	return nil
}

func (s *Server) getTeam(w http.ResponseWriter, r *http.Request) error {
	return s.writeTeam(w, r, chi.URLParam(r, "teamID"), http.StatusOK)
}

// canManageTeam: admins, and leads of the team or any team above it.
func (s *Server) canManageTeam(ctx context.Context, u User, teamID string) (bool, error) {
	if u.IsAdmin {
		return true, nil
	}
	var ok bool
	err := s.DB.QueryRow(ctx, `
		with recursive up as (
			select id, parent_id from teams where id = $1
			union all
			select t.id, t.parent_id from teams t join up on t.id = up.parent_id
		)
		select exists (select 1 from team_members m join up on up.id = m.team_id where m.user_id = $2 and m.role = 'lead')`,
		teamID, u.ID).Scan(&ok)
	return ok, err
}

func (s *Server) updateTeam(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "teamID")
	var in struct {
		Slug     *string `json:"slug"`
		Name     *string `json:"name"`
		ParentID *string `json:"parent_id"` // "" moves the team to the top level
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ok, err := s.canManageTeam(r.Context(), currentUser(r), id)
	if err != nil {
		return err
	}
	if !ok {
		return errForbidden
	}

	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if in.Slug != nil {
		if _, err := tx.Exec(r.Context(), `update teams set slug = $2 where id = $1`, id, strings.ToLower(strings.TrimSpace(*in.Slug))); err != nil {
			return err
		}
	}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return badRequest("name is required")
		}
		if _, err := tx.Exec(r.Context(), `update teams set name = $2 where id = $1`, id, strings.TrimSpace(*in.Name)); err != nil {
			return err
		}
	}
	if in.ParentID != nil {
		var parent *string
		if *in.ParentID != "" {
			parent = in.ParentID
			// Refuse to move a team under itself or one of its descendants.
			var cycle bool
			if err := tx.QueryRow(r.Context(), `
				with recursive down as (
					select id from teams where id = $1
					union all
					select t.id from teams t join down on t.parent_id = down.id
				)
				select exists (select 1 from down where id = $2)`, id, *parent).Scan(&cycle); err != nil {
				return err
			}
			if cycle {
				return badRequest("a team cannot be moved under itself or one of its sub-teams")
			}
		}
		if _, err := tx.Exec(r.Context(), `update teams set parent_id = $2 where id = $1`, id, parent); err != nil {
			return err
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	return s.writeTeam(w, r, id, http.StatusOK)
}

func (s *Server) putTeamMember(w http.ResponseWriter, r *http.Request) error {
	teamID, userID := chi.URLParam(r, "teamID"), chi.URLParam(r, "userID")
	var in struct {
		Role string `json:"role"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Role == "" {
		in.Role = "member"
	}
	ok, err := s.canManageTeam(r.Context(), currentUser(r), teamID)
	if err != nil {
		return err
	}
	if !ok {
		return errForbidden
	}
	if _, err := s.DB.Exec(r.Context(), `
		insert into team_members (team_id, user_id, role) values ($1, $2, $3)
		on conflict (team_id, user_id) do update set role = excluded.role`, teamID, userID, in.Role); err != nil {
		return err
	}
	return s.writeTeam(w, r, teamID, http.StatusOK)
}

func (s *Server) deleteTeamMember(w http.ResponseWriter, r *http.Request) error {
	teamID, userID := chi.URLParam(r, "teamID"), chi.URLParam(r, "userID")
	ok, err := s.canManageTeam(r.Context(), currentUser(r), teamID)
	if err != nil {
		return err
	}
	if !ok {
		return errForbidden
	}
	if _, err := s.DB.Exec(r.Context(), `delete from team_members where team_id = $1 and user_id = $2`, teamID, userID); err != nil {
		return err
	}
	return s.writeTeam(w, r, teamID, http.StatusOK)
}

// SpaceApp is an application as seen from a team space.
type SpaceApp struct {
	App
	// Relation is "owner" or the responsible role, and Via names the team in
	// the space (the team itself or one of its sub-teams) that holds it.
	Relation string `json:"relation"`
	ViaID    string `json:"via_id"`
	ViaName  string `json:"via_name"`
}

// teamSpace collects everything a team and its sub-teams own or are
// responsible for. It is a view computed from the ownership mapping: none of
// these resources live "under" the team, so their paths never change.
func (s *Server) teamSpace(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "teamID")
	ctx := r.Context()
	rows, err := s.DB.Query(ctx, `
		with recursive down as (
			select id from teams where id = $1
			union all
			select t.id from teams t join down on t.parent_id = down.id
		), rel as (
			select a.key, 'owner' as relation, a.owner_team_id as via from applications a join down on down.id = a.owner_team_id
			union all
			select at.app_key, at.role, at.team_id from application_teams at join down on down.id = at.team_id
		)
		select `+appCols+`, rel.relation, vt.id, vt.name
		  from rel
		  join applications a on a.key = rel.key
		  join teams ot on ot.id = a.owner_team_id
		  join teams vt on vt.id = rel.via
		 order by a.name, rel.relation`, id)
	if err != nil {
		return err
	}
	apps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SpaceApp, error) {
		var a SpaceApp
		err := row.Scan(&a.Key, &a.Name, &a.Description, &a.OwnerTeam.ID, &a.OwnerTeam.Name, &a.CreatedAt, &a.Relation, &a.ViaID, &a.ViaName)
		return a, err
	})
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(apps))
	for _, a := range apps {
		keys = append(keys, a.Key)
	}
	rows, err = s.DB.Query(ctx, `select `+issueCols+` from issues i
		left join users au on au.id = i.author_id left join users asg on asg.id = i.assignee_id
		where i.app_key = any($1) and i.status in ('open', 'in_progress')
		order by i.updated_at desc limit 100`, keys)
	if err != nil {
		return err
	}
	issues, err := scanIssues(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"applications": apps, "open_issues": issues})
	return nil
}

func (s *Server) myTeams(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `select `+teamCols+` from teams t
		join team_members m on m.team_id = t.id where m.user_id = $1 order by t.name`, currentUser(r).ID)
	if err != nil {
		return err
	}
	teams, err := scanTeams(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, teams)
	return nil
}
