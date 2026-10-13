package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
)

type Issue struct {
	ID        string    `json:"id"`
	AppKey    string    `json:"app_key"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	Author    *Author   `json:"author"`
	Assignee  *Author   `json:"assignee"`
	ChannelID string    `json:"channel_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const issueCols = `i.id, i.app_key, i.number, i.title, i.body, i.status,
	au.id, au.username, au.display_name, asg.id, asg.username, asg.display_name,
	i.channel_id, i.created_at, i.updated_at`

const issueFrom = ` from issues i left join users au on au.id = i.author_id left join users asg on asg.id = i.assignee_id `

func scanIssues(rows pgx.Rows) ([]Issue, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Issue, error) {
		var i Issue
		var aid, aun, adn, sid, sun, sdn *string
		err := row.Scan(&i.ID, &i.AppKey, &i.Number, &i.Title, &i.Body, &i.Status, &aid, &aun, &adn, &sid, &sun, &sdn,
			&i.ChannelID, &i.CreatedAt, &i.UpdatedAt)
		if aid != nil {
			i.Author = &Author{ID: *aid, Username: *aun, DisplayName: *adn}
		}
		if sid != nil {
			i.Assignee = &Author{ID: *sid, Username: *sun, DisplayName: *sdn}
		}
		return i, err
	})
}

var issueStatuses = map[string]bool{"open": true, "in_progress": true, "done": true, "closed": true}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	if _, err := s.requireRole(r, app, access.Reader); err != nil {
		return err
	}
	var statuses []string
	switch st := r.URL.Query().Get("status"); st {
	case "", "active":
		statuses = []string{"open", "in_progress"}
	case "all":
		statuses = []string{"open", "in_progress", "done", "closed"}
	case "finished":
		statuses = []string{"done", "closed"}
	default:
		if !issueStatuses[st] {
			return badRequest("unknown status")
		}
		statuses = []string{st}
	}
	rows, err := s.DB.Query(r.Context(), `select `+issueCols+issueFrom+` where i.app_key = $1 and i.status = any($2) order by i.number desc`, app, statuses)
	if err != nil {
		return err
	}
	issues, err := scanIssues(rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, issues)
	return nil
}

func (s *Server) issueByNumber(r *http.Request, app string, number int) (Issue, error) {
	rows, err := s.DB.Query(r.Context(), `select `+issueCols+issueFrom+` where i.app_key = $1 and i.number = $2`, app, number)
	if err != nil {
		return Issue{}, err
	}
	issues, err := scanIssues(rows)
	if err != nil {
		return Issue{}, err
	}
	if len(issues) == 0 {
		return Issue{}, errNotFound
	}
	return issues[0], nil
}

func (s *Server) writeIssue(w http.ResponseWriter, r *http.Request, app string, number, status int) error {
	issue, err := s.issueByNumber(r, app, number)
	if err != nil {
		return err
	}
	links, err := s.linksFor(r.Context(), Ref{Type: "issue", ID: issue.ID}, currentUser(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, status, map[string]any{"issue": issue, "links": links})
	return nil
}

func issueNumber(r *http.Request) (int, error) {
	n, err := strconv.Atoi(chi.URLParam(r, "number"))
	if err != nil {
		return 0, errNotFound
	}
	return n, nil
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	if _, err := s.requireRole(r, app, access.Reader); err != nil {
		return err
	}
	n, err := issueNumber(r)
	if err != nil {
		return err
	}
	return s.writeIssue(w, r, app, n, http.StatusOK)
}

// createIssue creates an issue, optionally from a message. Created from a
// message, the issue links back to it, its discussion starts with a quote of
// the source, and the source conversation gets a note pointing at the issue.
func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	var in struct {
		Title      string  `json:"title"`
		Body       string  `json:"body"`
		AssigneeID *string `json:"assignee_id"`
		Source     *Ref    `json:"source"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return badRequest("title is required")
	}
	if in.AssigneeID != nil && *in.AssigneeID == "" {
		in.AssigneeID = nil
	}
	if _, err := s.requireRole(r, app, access.Reader); err != nil {
		return err
	}
	ctx := r.Context()

	var source *Message
	if in.Source != nil {
		if in.Source.Type != "message" {
			return badRequest("issues can be created from messages")
		}
		m, err := s.visibleMessage(r, in.Source.ID)
		if err != nil {
			return err
		}
		source = &m
	}

	u := currentUser(r)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var number int
	if err := tx.QueryRow(ctx, `update applications set next_issue_number = next_issue_number + 1 where key = $1 returning next_issue_number - 1`, app).Scan(&number); err != nil {
		return err
	}
	var channelID, issueID string
	if err := tx.QueryRow(ctx, `insert into channels (app_key, kind, name) values ($1, 'issue', $2) returning id`,
		app, fmt.Sprintf("%s#%d", app, number)).Scan(&channelID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `insert into issues (app_key, number, title, body, author_id, assignee_id, channel_id)
		values ($1, $2, $3, $4, $5, $6, $7) returning id`, app, number, in.Title, in.Body, u.ID, in.AssigneeID, channelID).Scan(&issueID); err != nil {
		return err
	}

	var noteID string
	if source != nil {
		thread := source.ThreadID
		if thread == nil {
			thread = &source.ID
		}
		srcRef := Ref{Type: "message", ID: source.ID}
		if err := addLink(ctx, tx, Ref{Type: "issue", ID: issueID}, srcRef, LinkCreatedFrom,
			map[string]any{"channel_id": source.ChannelID, "thread_id": *thread}, u.ID); err != nil {
			return err
		}
		srcEntity, err := s.resolve(ctx, srcRef)
		if err != nil {
			return err
		}
		issueEntity := Entity{
			Ref:      Ref{Type: "issue", ID: issueID},
			Title:    in.Title,
			Subtitle: fmt.Sprintf("%s#%d · open", app, number),
			URL:      fmt.Sprintf("/apps/%s/issues/%d", app, number),
		}
		quote := map[string]any{"type": "entity", "entity": srcEntity, "quote": source.Body}
		if _, err := s.postSystemMessage(ctx, tx, channelID, nil, "Created from a conversation", []any{quote}, nil); err != nil {
			return err
		}
		noteID, err = s.postSystemMessage(ctx, tx, source.ChannelID, thread,
			fmt.Sprintf("%s created issue %s#%d from this conversation", u.DisplayName, app, number),
			[]any{map[string]any{"type": "entity", "entity": issueEntity}}, source.visibleTo)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if noteID != "" {
		if m, err := s.messageByID(ctx, noteID, ""); err == nil {
			s.publishMessage(ctx, m)
		}
	}
	return s.writeIssue(w, r, app, number, http.StatusCreated)
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	n, err := issueNumber(r)
	if err != nil {
		return err
	}
	var in struct {
		Title      *string `json:"title"`
		Body       *string `json:"body"`
		Status     *string `json:"status"`
		AssigneeID *string `json:"assignee_id"` // "" unassigns
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	role, err := s.requireRole(r, app, access.Reader)
	if err != nil {
		return err
	}
	issue, err := s.issueByNumber(r, app, n)
	if err != nil {
		return err
	}
	u := currentUser(r)
	// Authors can edit their own issues; everyone else needs developer.
	if role < access.Developer && (issue.Author == nil || issue.Author.ID != u.ID) {
		return errForbidden
	}

	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var events []string
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return badRequest("title is required")
		}
		if _, err := tx.Exec(ctx, `update issues set title = $2, updated_at = now() where id = $1`, issue.ID, t); err != nil {
			return err
		}
	}
	if in.Body != nil {
		if _, err := tx.Exec(ctx, `update issues set body = $2, updated_at = now() where id = $1`, issue.ID, *in.Body); err != nil {
			return err
		}
	}
	if in.Status != nil && *in.Status != issue.Status {
		if !issueStatuses[*in.Status] {
			return badRequest("unknown status")
		}
		if _, err := tx.Exec(ctx, `update issues set status = $2, updated_at = now() where id = $1`, issue.ID, *in.Status); err != nil {
			return err
		}
		events = append(events, fmt.Sprintf("%s changed the status to %s", u.DisplayName, strings.ReplaceAll(*in.Status, "_", " ")))
	}
	if in.AssigneeID != nil {
		var assignee *string
		if *in.AssigneeID != "" {
			assignee = in.AssigneeID
		}
		var name *string
		if _, err := tx.Exec(ctx, `update issues set assignee_id = $2, updated_at = now() where id = $1`, issue.ID, assignee); err != nil {
			return err
		}
		if assignee != nil {
			if err := tx.QueryRow(ctx, `select display_name from users where id = $1`, *assignee).Scan(&name); err != nil {
				return err
			}
			events = append(events, fmt.Sprintf("%s assigned %s", u.DisplayName, *name))
		} else {
			events = append(events, fmt.Sprintf("%s removed the assignee", u.DisplayName))
		}
	}
	var noteIDs []string
	for _, e := range events {
		id, err := s.postSystemMessage(ctx, tx, issue.ChannelID, nil, e, []any{}, nil)
		if err != nil {
			return err
		}
		noteIDs = append(noteIDs, id)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, id := range noteIDs {
		if m, err := s.messageByID(ctx, id, ""); err == nil {
			s.publishMessage(ctx, m)
		}
	}
	return s.writeIssue(w, r, app, n, http.StatusOK)
}
