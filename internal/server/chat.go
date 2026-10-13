package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

const channelCols = `c.id, c.app_key, c.kind, c.name, c.created_at`

func scanChannels(rows pgx.Rows) ([]Channel, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Channel, error) {
		var c Channel
		err := row.Scan(&c.ID, &c.AppKey, &c.Kind, &c.Name, &c.CreatedAt)
		return c, err
	})
}

// Author is the public view of a message author. Nil for system messages.
type Author struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type Message struct {
	ID          string          `json:"id"`
	ChannelID   string          `json:"channel_id"`
	ThreadID    *string         `json:"thread_id"`
	Author      *Author         `json:"author"`
	Body        string          `json:"body"`
	Attachments json.RawMessage `json:"attachments"`
	ReplyCount  int             `json:"reply_count"`
	LinkCount   int             `json:"link_count"`
	CreatedAt   time.Time       `json:"created_at"`
}

const messageCols = `m.id, m.channel_id, m.thread_id, u.id, u.username, u.display_name, m.body, m.attachments,
	(select count(*) from messages r where r.thread_id = m.id),
	(select count(*) from links l where (l.to_type = 'message' and l.to_id = m.id::text) or (l.from_type = 'message' and l.from_id = m.id::text)),
	m.created_at`

func scanMessages(rows pgx.Rows) ([]Message, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Message, error) {
		var m Message
		var uid, uname, dname *string
		err := row.Scan(&m.ID, &m.ChannelID, &m.ThreadID, &uid, &uname, &dname, &m.Body, &m.Attachments, &m.ReplyCount, &m.LinkCount, &m.CreatedAt)
		if uid != nil {
			m.Author = &Author{ID: *uid, Username: *uname, DisplayName: *dname}
		}
		return m, err
	})
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
	rows, err := s.DB.Query(r.Context(), `insert into channels (app_key, kind, name) values ($1, 'chat', $2)
		returning id, app_key, kind, name, created_at`, in.AppKey, in.Name)
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
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) error {
	ch, err := s.loadChannel(r.Context(), r, chi.URLParam(r, "channelID"))
	if err != nil {
		return err
	}
	var rows pgx.Rows
	if thread := r.URL.Query().Get("thread"); thread != "" {
		rows, err = s.DB.Query(r.Context(), `select `+messageCols+` from messages m left join users u on u.id = m.author_id
			where m.channel_id = $1 and (m.id = $2 or m.thread_id = $2) order by m.created_at`, ch.ID, thread)
	} else {
		rows, err = s.DB.Query(r.Context(), `select * from (select `+messageCols+` from messages m left join users u on u.id = m.author_id
			where m.channel_id = $1 and m.thread_id is null order by m.created_at desc limit 200) x order by created_at`, ch.ID)
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
	m, err := s.messageByID(r.Context(), chi.URLParam(r, "messageID"))
	if err != nil {
		return err
	}
	if _, err := s.loadChannel(r.Context(), r, m.ChannelID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, m)
	return nil
}

func (s *Server) messageByID(ctx context.Context, id string) (Message, error) {
	rows, err := s.DB.Query(ctx, `select `+messageCols+` from messages m left join users u on u.id = m.author_id where m.id = $1`, id)
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
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" && in.Code == nil {
		return badRequest("message is empty")
	}
	if in.ThreadID != nil && *in.ThreadID == "" {
		in.ThreadID = nil
	}
	if in.ThreadID != nil {
		// Replies hang off a top-level message in the same channel.
		var ok bool
		if err := s.DB.QueryRow(ctx, `select exists (select 1 from messages where id = $1 and channel_id = $2 and thread_id is null)`,
			*in.ThreadID, ch.ID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return badRequest("thread not found in this channel")
		}
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

	u := currentUser(r)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `insert into messages (channel_id, thread_id, author_id, body, attachments) values ($1, $2, $3, $4, $5) returning id`,
		ch.ID, in.ThreadID, u.ID, in.Body, attachments).Scan(&id); err != nil {
		return err
	}
	if code != nil {
		codeRef := Ref{Type: "code", ID: code.ID()}
		if err := addLink(ctx, tx, Ref{Type: "message", ID: id}, codeRef, LinkDiscusses, nil, u.ID); err != nil {
			return err
		}
		if ch.Kind == "issue" {
			var issueID string
			if err := tx.QueryRow(ctx, `select id from issues where channel_id = $1`, ch.ID).Scan(&issueID); err != nil {
				return err
			}
			if err := addLink(ctx, tx, Ref{Type: "issue", ID: issueID}, codeRef, LinkDiscusses, map[string]any{"message_id": id}, u.ID); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	m, err := s.messageByID(ctx, id)
	if err != nil {
		return err
	}
	s.publishMessage(ctx, m)
	writeJSON(w, http.StatusCreated, m)
	return nil
}

// publishMessage notifies channel subscribers, and refreshes the parent's
// reply count for thread replies.
func (s *Server) publishMessage(ctx context.Context, m Message) {
	s.Hub.Publish("channel:"+m.ChannelID, "message.created", m)
	if m.ThreadID != nil {
		if parent, err := s.messageByID(ctx, *m.ThreadID); err == nil {
			s.Hub.Publish("channel:"+m.ChannelID, "message.updated", parent)
		}
	}
}

// postSystemMessage adds a message without an author, used to note links
// (for example "issue created from this conversation") where they happened.
func (s *Server) postSystemMessage(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, channelID string, threadID *string, body string, attachments []any) (string, error) {
	var id string
	err := q.QueryRow(ctx, `insert into messages (channel_id, thread_id, author_id, body, attachments) values ($1, $2, null, $3, $4) returning id`,
		channelID, threadID, body, attachments).Scan(&id)
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
		case "channel":
			var app *string
			if err := s.DB.QueryRow(ctx, `select app_key from channels where id = $1`, id).Scan(&app); err != nil {
				return false
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
