// Package access resolves what a user may do on an application.
//
// Roles come from the ownership mapping, never from the application's path:
// members of the owning team are maintainers, members of responsible teams get
// the role the mapping assigns, direct grants cover exceptions, and every
// signed-in user can read (internal visibility). Renaming or moving a team
// therefore changes nothing here except who is a member of what.
package access

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Role is ordered: a higher role includes everything a lower one may do.
type Role int

const (
	None Role = iota * 10
	Reader
	Developer
	Maintainer
	Owner
)

func (r Role) String() string {
	switch r {
	case Reader:
		return "reader"
	case Developer:
		return "developer"
	case Maintainer:
		return "maintainer"
	case Owner:
		return "owner"
	}
	return "none"
}

// ParseRole maps a stored role name to a Role.
func ParseRole(s string) Role {
	switch s {
	case "reader":
		return Reader
	case "developer":
		return Developer
	case "maintainer":
		return Maintainer
	case "owner":
		return Owner
	}
	return None
}

// Resolver looks up roles in the database.
type Resolver struct {
	DB *pgxpool.Pool
}

// AppRole returns the user's effective role on the application.
func (r Resolver) AppRole(ctx context.Context, userID, appKey string) (Role, error) {
	var isAdmin bool
	if err := r.DB.QueryRow(ctx, `select is_admin from users where id = $1`, userID).Scan(&isAdmin); err != nil {
		return None, err
	}
	if isAdmin {
		return Owner, nil
	}

	var best int
	err := r.DB.QueryRow(ctx, `
		select coalesce(max(r), 0) from (
			select 30 as r
			  from applications a
			  join team_members tm on tm.team_id = a.owner_team_id
			 where a.key = $1 and tm.user_id = $2
			union all
			select case at.role when 'maintainer' then 30 else 20 end
			  from application_teams at
			  join team_members tm on tm.team_id = at.team_id
			 where at.app_key = $1 and tm.user_id = $2
			union all
			select case g.role when 'owner' then 40 when 'maintainer' then 30 when 'developer' then 20 else 10 end
			  from application_grants g
			 where g.app_key = $1 and g.user_id = $2
		) s`, appKey, userID).Scan(&best)
	if err != nil {
		return None, err
	}
	role := Role(best)
	if role < Reader {
		role = Reader
	}
	return role, nil
}
