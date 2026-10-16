package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
)

type Channel struct {
	ID        string    `json:"id"`
	AppKey    *string   `json:"app_key"`
	Kind      string    `json:"kind"` // "chat", "issue" or "direct"
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// Direct channels are one person's private chat with one agent.
	OwnerID       *string    `json:"owner_id,omitempty"`
	Agent         *string    `json:"agent,omitempty"`
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
}

const channelCols = `c.id, c.app_key, c.kind, c.name, c.created_at, c.owner_id,
	(select a.username from users a where a.id = c.agent_id), c.last_message_at`

func scanChannels(rows pgx.Rows) ([]Channel, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Channel, error) {
		var c Channel
		err := row.Scan(&c.ID, &c.AppKey, &c.Kind, &c.Name, &c.CreatedAt, &c.OwnerID, &c.Agent, &c.LastMessageAt)
		return c, err
	})
}

// direct reports whether the channel is a private chat with an agent.
func (c Channel) direct() bool { return c.Kind == "direct" }

// Author is the public view of a message author. Nil for system messages.
type Author struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"` // "human" or "agent"
}

type Message struct {
	ID          string          `json:"id"`
	ChannelID   string          `json:"channel_id"`
	ThreadID    *string         `json:"thread_id"`
	Author      *Author         `json:"author"`
	Body        string          `json:"body"`
	Attachments json.RawMessage `json:"attachments"`
	// Private messages are visible only to one user ("answer only me").
	Private    bool      `json:"private"`
	ReplyCount int       `json:"reply_count"`
	LinkCount  int       `json:"link_count"`
	CreatedAt  time.Time `json:"created_at"`

	visibleTo *string
}

// messageCols selects messages as one viewer sees them: reply counts include
// only replies the viewer can see. viewer is the placeholder bound to the
// viewer's user ID, or to nil for what everyone sees.
func messageCols(viewer string) string {
	return `m.id, m.channel_id, m.thread_id, u.id, u.username, u.display_name, u.kind, m.body, m.attachments, m.visible_to,
	(select count(*) from messages r where r.thread_id = m.id and (r.visible_to is null or r.visible_to = ` + viewer + `::uuid)),
	(select count(*) from links l where (l.to_type = 'message' and l.to_id = m.id::text) or (l.from_type = 'message' and l.from_id = m.id::text)),
	m.created_at`
}

// visibleTo is the condition for messages the viewer may see.
func visibleTo(viewer string) string {
	return `(m.visible_to is null or m.visible_to = ` + viewer + `::uuid)`
}

// viewerArg binds a viewer ID; "" means the public view.
func viewerArg(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func scanMessages(rows pgx.Rows) ([]Message, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Message, error) {
		var m Message
		var uid, uname, dname, kind *string
		err := row.Scan(&m.ID, &m.ChannelID, &m.ThreadID, &uid, &uname, &dname, &kind, &m.Body, &m.Attachments, &m.visibleTo,
			&m.ReplyCount, &m.LinkCount, &m.CreatedAt)
		if uid != nil {
			m.Author = &Author{ID: *uid, Username: *uname, DisplayName: *dname, Kind: *kind}
		}
		m.Private = m.visibleTo != nil
		return m, err
	})
}

// visibleFor reports whether the user may see the message.
func (m Message) visibleFor(userID string) bool {
	return m.visibleTo == nil || *m.visibleTo == userID
}

func (s *Server) loadChannel(ctx context.Context, r *http.Request, id string) (Channel, error) {
	rows, err := s.DB.Query(ctx, `select `+channelCols+` from channels c where c.id = $1`, id)
	if err != nil {
		return Channel{}, err
	}
	chs, err := scanChannels(rows)
	if err != nil {
		return Channel{}, err
	}
	if len(chs) == 0 {
		return Channel{}, errNotFound
	}
	if chs[0].direct() && *chs[0].OwnerID != currentUser(r).ID {
		return Channel{}, errNotFound
	}
	if chs[0].AppKey != nil {
		if _, err := s.requireRole(r, *chs[0].AppKey, access.Reader); err != nil {
			return Channel{}, err
		}
	}
	return chs[0], nil
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) error {
	app := r.URL.Query().Get("app")
	var rows pgx.Rows
	var err error
	if app != "" {
		rows, err = s.DB.Query(r.Context(), `select `+channelCols+` from channels c where c.kind = 'chat' and c.app_key = $1 order by c.name`, app)
	} else {
		rows, err = s.DB.Query(r.Context(), `select `+channelCols+` from channels c where c.kind = 'chat' order by c.app_key nulls first, c.name`)
	}
	if err != nil {
		return err
	}
	chs, err := scanChannels(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, chs)
	return nil
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		AppKey *string `json:"app_key"`
		Name   string  `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(in.Name, "#")))
	if in.Name == "" {
		return badRequest("name is required")
	}
	if in.AppKey != nil && *in.AppKey == "" {
		in.AppKey = nil
	}
	if in.AppKey != nil {
		if _, err := s.requireRole(r, *in.AppKey, access.Developer); err != nil {
			return err
		}
	}
	rows, err := s.DB.Query(r.Context(), `insert into channels as c (app_key, kind, name) values ($1, 'chat', $2)
		returning `+channelCols, in.AppKey, in.Name)
	if err != nil {
		return err
	}
	chs, err := scanChannels(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, chs[0])
	return nil
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) error {
	ch, err := s.loadChannel(r.Context(), r, chi.URLParam(r, "channelID"))
	if err != nil {
		return err
	}
	out := map[string]any{"channel": ch}
	if ch.Kind == "issue" {
		var app string
		var number int
		if err := s.DB.QueryRow(r.Context(), `select app_key, number from issues where channel_id = $1`, ch.ID).Scan(&app, &number); err == nil {
			out["issue_url"] = fmt.Sprintf("/apps/%s/issues/%d", app, number)
		}
	}
	if mr, err := s.mergeRequestForChannel(r.Context(), ch); err == nil {
		out["merge_request_url"] = mr.url()
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) error {
	ch, err := s.loadChannel(r.Context(), r, chi.URLParam(r, "channelID"))
	if err != nil {
		return err
	}
	me := currentUser(r).ID
	var rows pgx.Rows
	if thread := r.URL.Query().Get("thread"); thread != "" {
		rows, err = s.DB.Query(r.Context(), `select `+messageCols("$3")+` from messages m left join users u on u.id = m.author_id
			where m.channel_id = $1 and (m.id = $2 or m.thread_id = $2) and `+visibleTo("$3")+` order by m.created_at`, ch.ID, thread, me)
	} else {
		rows, err = s.DB.Query(r.Context(), `select * from (select `+messageCols("$2")+` from messages m left join users u on u.id = m.author_id
			where m.channel_id = $1 and m.thread_id is null and `+visibleTo("$2")+` order by m.created_at desc limit 200) x order by created_at`, ch.ID, me)
	}
	if err != nil {
		return err
	}
	msgs, err := scanMessages(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, msgs)
	return nil
}

func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) error {
	m, err := s.visibleMessage(r, chi.URLParam(r, "messageID"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, m)
	return nil
}

// visibleMessage loads a message the current user may see: its channel is
// readable and it is not someone else's private message.
func (s *Server) visibleMessage(r *http.Request, id string) (Message, error) {
	u := currentUser(r)
	m, err := s.messageByID(r.Context(), id, u.ID)
	if err != nil {
		return Message{}, err
	}
	if !m.visibleFor(u.ID) {
		return Message{}, errNotFound
	}
	if _, err := s.loadChannel(r.Context(), r, m.ChannelID); err != nil {
		return Message{}, err
	}
	return m, nil
}

// messageByID loads a message as viewer sees it ("" for the public view). It
// does not check visibility; see visibleMessage.
func (s *Server) messageByID(ctx context.Context, id, viewer string) (Message, error) {
	rows, err := s.DB.Query(ctx, `select `+messageCols("$2")+` from messages m left join users u on u.id = m.author_id where m.id = $1`, id, viewerArg(viewer))
	if err != nil {
		return Message{}, err
	}
	msgs, err := scanMessages(rows)
	if err != nil {
		return Message{}, err
	}
	if len(msgs) == 0 {
		return Message{}, errNotFound
	}
	return msgs[0], nil
}

// CodeInput selects a line range to discuss. Ref can be a branch, tag or
// commit; it is pinned to the commit it resolves to.
type CodeInput struct {
	App   string `json:"app"`
	Repo  string `json:"repo"`
	Ref   string `json:"ref"`
	Path  string `json:"path"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

const maxSnippetLines = 200

// codeAttachment pins the range to a commit and captures the lines so the
// conversation still shows what was discussed after the file changes.
func (s *Server) codeAttachment(r *http.Request, in CodeInput) (CodeRange, map[string]any, error) {
	if in.Start < 1 || in.End < in.Start || in.End-in.Start >= maxSnippetLines {
		return CodeRange{}, nil, badRequest(fmt.Sprintf("select between 1 and %d lines", maxSnippetLines))
	}
	if _, err := s.requireRole(r, in.App, access.Reader); err != nil {
		return CodeRange{}, nil, err
	}
	var exists bool
	if err := s.DB.QueryRow(r.Context(), `select exists (select 1 from repositories where app_key = $1 and name = $2)`, in.App, in.Repo).Scan(&exists); err != nil {
		return CodeRange{}, nil, err
	}
	if !exists {
		return CodeRange{}, nil, errNotFound
	}
	commit, err := s.Git.ResolveCommit(r.Context(), in.App, in.Repo, in.Ref)
	if err != nil {
		return CodeRange{}, nil, errStatus(http.StatusNotFound, "ref not found")
	}
	lines, err := s.Git.Lines(r.Context(), in.App, in.Repo, commit, in.Path, in.Start, in.End)
	if err != nil {
		return CodeRange{}, nil, gitErr(err)
	}
	c := CodeRange{App: in.App, Repo: in.Repo, Commit: commit, Path: strings.Trim(in.Path, "/"), Start: in.Start, End: in.Start + len(lines) - 1}
	att := map[string]any{
		"type":  "code",
		"ref":   Ref{Type: "code", ID: c.ID()},
		"code":  c,
		"lines": lines,
		"url":   c.url(),
	}
	return c, att, nil
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	ch, err := s.loadChannel(ctx, r, chi.URLParam(r, "channelID"))
	if err != nil {
		return err
	}
	var in struct {
		Body     string     `json:"body"`
		ThreadID *string    `json:"thread_id"`
		Code     *CodeInput `json:"code"`
		// Private keeps the message, and any agent answer to it, visible to
		// the author only ("answer only me").
		Private bool `json:"private"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u := currentUser(r)
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" && in.Code == nil {
		return badRequest("message is empty")
	}
	if in.ThreadID != nil && *in.ThreadID == "" {
		in.ThreadID = nil
	}
	privateThread := false
	if in.ThreadID != nil {
		// Replies hang off a top-level message in the same channel that the
		// author can see. Replies under a private message stay private.
		var ok bool
		var rootVisibleTo *string
		err := s.DB.QueryRow(ctx, `select true, visible_to from messages where id = $1 and channel_id = $2 and thread_id is null`,
			*in.ThreadID, ch.ID).Scan(&ok, &rootVisibleTo)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && rootVisibleTo != nil && *rootVisibleTo != u.ID) {
			return badRequest("thread not found in this channel")
		}
		if err != nil {
			return err
		}
		if rootVisibleTo != nil {
			in.Private, privateThread = true, true
		}
	}
	mentioned := s.agents.mentioned(in.Body)
	if ch.direct() {
		// Everything in a private chat is the owner's alone, and the chat's
		// agent answers every message without being mentioned.
		in.Private = true
		mentioned = nil
		if def, ok := s.agents.byUsername(*ch.Agent); ok {
			mentioned = append(mentioned, def)
		}
	} else if in.Private && !privateThread && len(mentioned) == 0 {
		return badRequest("private messages are for asking an agent: mention one, for example @assistant")
	}

	attachments := []any{}
	var code *CodeRange
	if in.Code != nil {
		c, att, err := s.codeAttachment(r, *in.Code)
		if err != nil {
			return err
		}
		code = &c
		attachments = append(attachments, att)
	}

	var visible *string
	if in.Private {
		visible = &u.ID
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `insert into messages (channel_id, thread_id, author_id, body, attachments, visible_to) values ($1, $2, $3, $4, $5, $6) returning id`,
		ch.ID, in.ThreadID, u.ID, in.Body, attachments, visible).Scan(&id); err != nil {
		return err
	}
	if code != nil {
		codeRef := Ref{Type: "code", ID: code.ID()}
		if err := addLink(ctx, tx, Ref{Type: "message", ID: id}, codeRef, LinkDiscusses, nil, u.ID); err != nil {
			return err
		}
		var owner Ref
		switch ch.Kind {
		case "issue":
			owner.Type = "issue"
			err = tx.QueryRow(ctx, `select id from issues where channel_id = $1`, ch.ID).Scan(&owner.ID)
		case "merge_request":
			owner.Type = "merge_request"
			err = tx.QueryRow(ctx, `select id from merge_requests where channel_id = $1`, ch.ID).Scan(&owner.ID)
		}
		if err != nil {
			return err
		}
		if owner.Type != "" {
			if err := addLink(ctx, tx, owner, codeRef, LinkDiscusses, map[string]any{"message_id": id}, u.ID); err != nil {
				return err
			}
		}
	}
	if ch.direct() {
		// A new chat takes its title from its first message.
		if err := tx.QueryRow(ctx, `update channels set last_message_at = now(),
			name = case when name = '' and $2::uuid is null then $3 else name end
			where id = $1 returning name, last_message_at`, ch.ID, in.ThreadID, chatTitle(in.Body)).Scan(&ch.Name, &ch.LastMessageAt); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if ch.direct() {
		s.Hub.Publish("user:"+u.ID, "direct.updated", ch)
	}
	m, err := s.messageByID(ctx, id, u.ID)
	if err != nil {
		return err
	}
	s.publishMessage(ctx, m)
	for _, def := range mentioned {
		start := s.startRun
		if def.Runs == "job" {
			start = s.startAgentJob
		}
		if err := start(ctx, def, u, ch, m); err != nil {
			slog.Error("start agent run", "agent", def.Name, "err", err)
		}
	}
	writeJSON(w, http.StatusCreated, m)
	return nil
}

// publishMessage notifies subscribers, and refreshes the parent's reply count
// for thread replies. Private messages go only to their one reader.
func (s *Server) publishMessage(ctx context.Context, m Message) {
	s.publishMessageEvent(ctx, "message.created", m)
	if m.ThreadID != nil {
		viewer := ""
		if m.visibleTo != nil {
			viewer = *m.visibleTo
		}
		if parent, err := s.messageByID(ctx, *m.ThreadID, viewer); err == nil {
			s.publishMessageEvent(ctx, "message.updated", parent)
			if viewer != "" && parent.visibleTo == nil {
				// The reader of a private reply sees a reply count that
				// includes it; send the parent to them as well.
				s.Hub.Publish("user:"+viewer, "message.updated", parent)
			}
		}
	}
}

func (s *Server) publishMessageEvent(ctx context.Context, typ string, m Message) {
	if m.visibleTo != nil {
		s.Hub.Publish("user:"+*m.visibleTo, typ, m)
		return
	}
	if typ == "message.updated" && m.ThreadID == nil {
		// Recompute the public reply count: m may have been loaded for a
		// viewer with private replies.
		if pub, err := s.messageByID(ctx, m.ID, ""); err == nil {
			m = pub
		}
	}
	s.Hub.Publish("channel:"+m.ChannelID, typ, m)
}

// postSystemMessage adds a message without an author, used to note links
// (for example "issue created from this conversation") where they happened.
// visible limits it to one user, for notes in a private thread.
func (s *Server) postSystemMessage(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, channelID string, threadID *string, body string, attachments []any, visible *string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `insert into messages (channel_id, thread_id, author_id, body, attachments, visible_to) values ($1, $2, null, $3, $4, $5) returning id`,
		channelID, threadID, body, attachments, visible).Scan(&id)
	return id, err
}

// serveEvents upgrades to a WebSocket. Clients subscribe to topics:
// "channel:<id>" for messages, "user:<id>" (their own) for todos.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	s.Hub.Serve(w, r, func(ctx context.Context, topic string) bool {
		kind, id, _ := strings.Cut(topic, ":")
		switch kind {
		case "user":
			return id == u.ID
		case "repo":
			var app string
			if err := s.DB.QueryRow(ctx, `select app_key from repositories where id = $1`, id).Scan(&app); err != nil {
				return false
			}
			role, err := s.Access.AppRole(ctx, u.ID, app)
			return err == nil && role >= access.Reader
		case "job":
			var app string
			if err := s.DB.QueryRow(ctx, `select r.app_key from jobs j join pipelines p on p.id = j.pipeline_id join repositories r on r.id = p.repo_id where j.id = $1`, id).Scan(&app); err != nil {
				return false
			}
			role, err := s.Access.AppRole(ctx, u.ID, app)
			return err == nil && role >= access.Reader
		case "channel":
			var app, owner *string
			if err := s.DB.QueryRow(ctx, `select app_key, owner_id from channels where id = $1`, id).Scan(&app, &owner); err != nil {
				return false
			}
			if owner != nil {
				return *owner == u.ID
			}
			if app == nil {
				return true
			}
			role, err := s.Access.AppRole(ctx, u.ID, *app)
			return err == nil && role >= access.Reader
		}
		return false
	})
}
