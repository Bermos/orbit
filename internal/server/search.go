package server

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
)

// SearchResult is one hit of a full-text search.
type SearchResult struct {
	Entity  Entity  `json:"entity"`
	Snippet string  `json:"snippet"`
	Rank    float32 `json:"rank"`
}

const maxSearchResults = 30

// search finds applications, issues and messages the user can see, using
// Postgres full-text search. Embeddings can come later behind the same API.
func (s *Server) search(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		return badRequest("q is required")
	}
	app := r.URL.Query().Get("app")
	var appArg any
	if app != "" {
		if _, err := s.requireRole(r, app, access.Reader); err != nil {
			return err
		}
		appArg = app
	}
	u := currentUser(r)

	type hit struct {
		ref     Ref
		snippet string
		rank    float32
	}
	var hits []hit
	collect := func(sql string, args ...any) error {
		rows, err := s.DB.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
			var h hit
			err := row.Scan(&h.ref.Type, &h.ref.ID, &h.snippet, &h.rank)
			return h, err
		})
		hits = append(hits, got...)
		return err
	}
	const headline = `'MaxWords=30, MinWords=12, ShortWord=2, StartSel=**, StopSel=**'`
	if err := collect(`
		select 'app', a.key, ts_headline('english', a.name || ' ' || a.description, query, `+headline+`),
		       ts_rank(to_tsvector('english', a.key || ' ' || a.name || ' ' || a.description), query)
		  from applications a, websearch_to_tsquery('english', $1) query
		 where to_tsvector('english', a.key || ' ' || a.name || ' ' || a.description) @@ query
		   and ($2::text is null or a.key = $2)
		 limit $3`, q, appArg, maxSearchResults); err != nil {
		return err
	}
	if err := collect(`
		select 'issue', i.id::text, ts_headline('english', i.title || ' ' || i.body, query, `+headline+`),
		       ts_rank(to_tsvector('english', i.title || ' ' || i.body), query)
		  from issues i, websearch_to_tsquery('english', $1) query
		 where to_tsvector('english', i.title || ' ' || i.body) @@ query
		   and ($2::text is null or i.app_key = $2)
		 order by 4 desc limit $3`, q, appArg, maxSearchResults); err != nil {
		return err
	}
	if err := collect(`
		select 'message', m.id::text, ts_headline('english', m.body, query, `+headline+`),
		       ts_rank(to_tsvector('english', m.body), query)
		  from messages m join channels c on c.id = m.channel_id, websearch_to_tsquery('english', $1) query
		 where to_tsvector('english', m.body) @@ query
		   and (m.visible_to is null or m.visible_to = $4)
		   and ($2::text is null or c.app_key = $2)
		 order by 4 desc limit $3`, q, appArg, maxSearchResults, u.ID); err != nil {
		return err
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank > hits[j].rank })

	roles := map[string]access.Role{}
	canRead := func(ctx context.Context, app string) (bool, error) {
		if app == "" {
			return true, nil
		}
		role, ok := roles[app]
		if !ok {
			var err error
			if role, err = s.Access.AppRole(ctx, u.ID, app); err != nil {
				return false, err
			}
			roles[app] = role
		}
		return role >= access.Reader, nil
	}
	out := []SearchResult{}
	for _, h := range hits {
		e, err := s.resolve(ctx, h.ref)
		if err != nil {
			return err
		}
		ok, err := canRead(ctx, e.App)
		if err != nil {
			return err
		}
		if e.Missing || !ok || !e.visibleFor(u.ID) {
			continue
		}
		out = append(out, SearchResult{Entity: e, Snippet: h.snippet, Rank: h.rank})
		if len(out) == maxSearchResults {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
