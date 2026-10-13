package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Ref points at any entity in Orbit. It is what the link graph stores.
type Ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Entity is a Ref resolved into something the UI can show and open.
type Entity struct {
	Ref
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	URL      string `json:"url"`
	Missing  bool   `json:"missing,omitempty"`
	// App is the application the entity belongs to, if any.
	App string `json:"app,omitempty"`
	// Private marks a message visible to one user only.
	Private bool `json:"private,omitempty"`

	// owner is who may see a personal entity (a to-do, a private message).
	owner *string
}

// visibleFor reports whether the user may see the entity at all.
func (e Entity) visibleFor(userID string) bool {
	return e.owner == nil || *e.owner == userID
}

// Link kinds used by the built-in flows.
const (
	LinkCreatedFrom = "created_from" // todo or issue created from a message
	LinkDiscusses   = "discusses"    // message discusses a code range
	LinkProposedBy  = "proposed_by"  // entity an agent drafted and a person confirmed
	LinkMentions    = "mentions"
	LinkRelates     = "relates_to"
)

// CodeRange is a line range in a file pinned to a commit, so it stays valid
// when the file changes later.
type CodeRange struct {
	App    string `json:"app"`
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
	Path   string `json:"path"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// ID encodes the range as "app/repo@commit:path#Lstart-Lend".
func (c CodeRange) ID() string {
	return fmt.Sprintf("%s/%s@%s:%s#L%d-L%d", c.App, c.Repo, c.Commit, c.Path, c.Start, c.End)
}

func parseCodeRange(id string) (CodeRange, error) {
	var c CodeRange
	repoPart, rest, ok := strings.Cut(id, "@")
	if !ok {
		return c, errors.New("invalid code ref")
	}
	c.App, c.Repo, ok = strings.Cut(repoPart, "/")
	if !ok {
		return c, errors.New("invalid code ref")
	}
	c.Commit, rest, ok = strings.Cut(rest, ":")
	if !ok {
		return c, errors.New("invalid code ref")
	}
	i := strings.LastIndex(rest, "#L")
	if i < 0 {
		return c, errors.New("invalid code ref")
	}
	c.Path = rest[:i]
	a, b, _ := strings.Cut(rest[i+2:], "-L")
	var err error
	if c.Start, err = strconv.Atoi(a); err != nil {
		return c, errors.New("invalid code ref")
	}
	if c.End, err = strconv.Atoi(b); err != nil {
		c.End = c.Start
	}
	return c, nil
}

func (c CodeRange) url() string {
	return fmt.Sprintf("/apps/%s/repos/%s/blob/%s/%s?lines=%d-%d", c.App, c.Repo, c.Commit, c.Path, c.Start, c.End)
}

// resolve turns a Ref into an Entity. Entities that no longer exist come back
// marked Missing so the UI can still show the dangling link.
func (s *Server) resolve(ctx context.Context, ref Ref) (Entity, error) {
	e := Entity{Ref: ref}
	var err error
	switch ref.Type {
	case "message":
		var body, channelID, kind, channelName string
		var appKey *string
		var threadID *string
		var issueNumber *int
		err = s.DB.QueryRow(ctx, `
			select m.body, m.channel_id, m.thread_id, c.kind, c.name, c.app_key, i.number, m.visible_to
			  from messages m join channels c on c.id = m.channel_id
			  left join issues i on i.channel_id = c.id
			 where m.id = $1`, ref.ID).Scan(&body, &channelID, &threadID, &kind, &channelName, &appKey, &issueNumber, &e.owner)
		if err != nil {
			break
		}
		e.Private = e.owner != nil
		if appKey != nil {
			e.App = *appKey
		}
		e.Title = strings80(body)
		q := url.Values{"message": {ref.ID}}
		if threadID != nil {
			q.Set("thread", *threadID)
		}
		if kind == "issue" && appKey != nil && issueNumber != nil {
			e.Subtitle = fmt.Sprintf("%s#%d", *appKey, *issueNumber)
			e.URL = fmt.Sprintf("/apps/%s/issues/%d?%s", *appKey, *issueNumber, q.Encode())
		} else {
			e.Subtitle = "#" + channelName
			if appKey != nil {
				e.Subtitle = *appKey + " #" + channelName
			}
			e.URL = fmt.Sprintf("/channels/%s?%s", channelID, q.Encode())
		}
	case "issue":
		var app, title, status string
		var number int
		err = s.DB.QueryRow(ctx, `select app_key, number, title, status from issues where id = $1`, ref.ID).Scan(&app, &number, &title, &status)
		if err != nil {
			break
		}
		e.Title = title
		e.Subtitle = fmt.Sprintf("%s#%d · %s", app, number, strings.ReplaceAll(status, "_", " "))
		e.URL = fmt.Sprintf("/apps/%s/issues/%d", app, number)
		e.App = app
	case "todo":
		var title string
		var done bool
		err = s.DB.QueryRow(ctx, `select title, done, user_id from todos where id = $1`, ref.ID).Scan(&title, &done, &e.owner)
		if err != nil {
			break
		}
		e.Title = title
		e.Subtitle = "todo"
		if done {
			e.Subtitle = "todo · done"
		}
		e.URL = "/todos"
	case "code":
		c, perr := parseCodeRange(ref.ID)
		if perr != nil {
			return e, badRequest(perr.Error())
		}
		e.Title = fmt.Sprintf("%s:%d-%d", c.Path, c.Start, c.End)
		short := c.Commit
		if len(short) > 8 {
			short = short[:8]
		}
		e.Subtitle = fmt.Sprintf("%s/%s @ %s", c.App, c.Repo, short)
		e.URL = c.url()
		e.App = c.App
	case "repo":
		app, name, ok := strings.Cut(ref.ID, "/")
		if !ok {
			return e, badRequest("invalid repo ref")
		}
		e.Title, e.Subtitle, e.URL, e.App = ref.ID, "repository", "/apps/"+app+"/repos/"+name, app
	case "app":
		var name string
		err = s.DB.QueryRow(ctx, `select name from applications where key = $1`, ref.ID).Scan(&name)
		e.Title, e.Subtitle, e.URL, e.App = name, ref.ID, "/apps/"+ref.ID, ref.ID
	case "agent_run":
		var agentName, status string
		var channelID string
		var reply, appKey *string
		err = s.DB.QueryRow(ctx, `
			select u.username, r.status, r.channel_id, r.reply_message_id, case when r.private then r.asker_id end, c.app_key
			  from agent_runs r join users u on u.id = r.agent_id join channels c on c.id = r.channel_id
			 where r.id = $1`, ref.ID).Scan(&agentName, &status, &channelID, &reply, &e.owner, &appKey)
		if err != nil {
			break
		}
		e.Title = "@" + agentName + " answer"
		e.Subtitle = "agent run · " + status
		e.Private = e.owner != nil
		if appKey != nil {
			e.App = *appKey
		}
		e.URL = "/channels/" + channelID
		if reply != nil {
			if target, rerr := s.resolve(ctx, Ref{Type: "message", ID: *reply}); rerr == nil && !target.Missing {
				e.URL = target.URL
			}
		}
	default:
		return e, badRequest("unknown entity type " + ref.Type)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		e.Missing = true
		e.Title = "(deleted " + ref.Type + ")"
		return e, nil
	}
	return e, err
}

// execer is satisfied by both the pool and a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// addLink records an edge in the link graph. Duplicates are ignored.
func addLink(ctx context.Context, q execer, from, to Ref, kind string, linkCtx map[string]any, by string) error {
	if linkCtx == nil {
		linkCtx = map[string]any{}
	}
	_, err := q.Exec(ctx, `
		insert into links (from_type, from_id, to_type, to_id, kind, context, created_by)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict do nothing`, from.Type, from.ID, to.Type, to.ID, kind, linkCtx, by)
	return err
}

// LinkView is one edge as seen from the entity it was requested for.
type LinkView struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Direction string         `json:"direction"` // "out": this entity points at Entity; "in": Entity points here
	Entity    Entity         `json:"entity"`
	Context   map[string]any `json:"context"`
	CreatedAt time.Time      `json:"created_at"`
}

// linksFor lists the entity's links as viewer sees them: other people's
// to-dos and private messages are left out.
func (s *Server) linksFor(ctx context.Context, ref Ref, viewer string) ([]LinkView, error) {
	rows, err := s.DB.Query(ctx, `
		select id, kind, 'out', to_type, to_id, context, created_at from links where from_type = $1 and from_id = $2
		union all
		select id, kind, 'in', from_type, from_id, context, created_at from links where to_type = $1 and to_id = $2
		order by created_at`, ref.Type, ref.ID)
	if err != nil {
		return nil, err
	}
	type raw struct {
		LinkView
		other Ref
	}
	raws, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (raw, error) {
		var x raw
		err := row.Scan(&x.ID, &x.Kind, &x.Direction, &x.other.Type, &x.other.ID, &x.Context, &x.CreatedAt)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]LinkView, 0, len(raws))
	for _, x := range raws {
		e, err := s.resolve(ctx, x.other)
		if err != nil {
			return nil, err
		}
		if !e.visibleFor(viewer) {
			continue
		}
		x.Entity = e
		out = append(out, x.LinkView)
	}
	return out, nil
}

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request) error {
	ref := Ref{Type: r.URL.Query().Get("type"), ID: r.URL.Query().Get("id")}
	if ref.Type == "" || ref.ID == "" {
		return badRequest("type and id are required")
	}
	links, err := s.linksFor(r.Context(), ref, currentUser(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, links)
	return nil
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		From Ref    `json:"from"`
		To   Ref    `json:"to"`
		Kind string `json:"kind"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = LinkRelates
	}
	ctx := r.Context()
	for _, ref := range []Ref{in.From, in.To} {
		e, err := s.resolve(ctx, ref)
		if err != nil {
			return err
		}
		if e.Missing || !e.visibleFor(currentUser(r).ID) {
			return badRequest(ref.Type + " does not exist")
		}
	}
	if err := addLink(ctx, s.DB, in.From, in.To, in.Kind, nil, currentUser(r).ID); err != nil {
		return err
	}
	links, err := s.linksFor(ctx, in.From, currentUser(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, links)
	return nil
}
