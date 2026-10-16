package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Bermos/orbit/internal/access"
	"github.com/Bermos/orbit/internal/gitrepo"
)

// Merge requests propose merging a source branch into a target branch of
// the same repository. They are numbered per application next to issues
// (ledger!3), and like an issue each has a discussion channel. Inline
// comments on the diff are messages in that channel carrying the code range
// they discuss, pinned to the source tip (new side) or the merge base (old
// side), so the review is part of the same conversation: it has threads,
// agents answer mentions in it, and any comment can become a to-do or issue.
//
// The source tip and merge base are kept up to date on every push, the
// combined state of the checks on the tip gates the merge, and merging
// closes the issues the description says it closes.

// MergeRequest is a merge request as the API shows it.
type MergeRequest struct {
	ID             string     `json:"id"`
	AppKey         string     `json:"app_key"`
	Repo           string     `json:"repo"`
	RepoID         string     `json:"repo_id"`
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	SourceBranch   string     `json:"source_branch"`
	TargetBranch   string     `json:"target_branch"`
	Status         string     `json:"status"` // "open", "merged" or "closed"
	Draft          bool       `json:"draft"`
	Author         *Author    `json:"author"`
	ChannelID      string     `json:"channel_id"`
	HeadSHA        string     `json:"head_sha"`
	BaseSHA        string     `json:"base_sha"`
	MergeCommitSHA *string    `json:"merge_commit_sha"`
	MergedBy       *Author    `json:"merged_by"`
	MergedAt       *time.Time `json:"merged_at"`
	ClosedAt       *time.Time `json:"closed_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// ChecksState is the combined state of the checks on the source tip, in
	// lists.
	ChecksState string `json:"checks_state"`
}

// Ref is the merge request as a link graph node.
func (mr MergeRequest) Ref() Ref { return Ref{Type: "merge_request", ID: mr.ID} }

func (mr MergeRequest) url() string {
	return fmt.Sprintf("/apps/%s/merge-requests/%d", mr.AppKey, mr.Number)
}

func (mr MergeRequest) reference() string { return fmt.Sprintf("%s!%d", mr.AppKey, mr.Number) }

const mrCols = `mr.id, mr.app_key, r.name, mr.repo_id, mr.number, mr.title, mr.body, mr.source_branch, mr.target_branch, mr.status, mr.draft,
	au.id, au.username, au.display_name, au.kind, mr.channel_id, mr.head_sha, mr.base_sha, mr.merge_commit_sha,
	mb.id, mb.username, mb.display_name, mb.kind, mr.merged_at, mr.closed_at, mr.created_at, mr.updated_at`

const mrFrom = ` from merge_requests mr join repositories r on r.id = mr.repo_id
	left join users au on au.id = mr.author_id left join users mb on mb.id = mr.merged_by `

func scanAuthor(id, username, name, kind *string) *Author {
	if id == nil {
		return nil
	}
	return &Author{ID: *id, Username: *username, DisplayName: *name, Kind: *kind}
}

func scanMergeRequests(rows pgx.Rows) ([]MergeRequest, error) {
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (MergeRequest, error) {
		var m MergeRequest
		var aid, aun, adn, ak, mid, mun, mdn, mk *string
		err := row.Scan(&m.ID, &m.AppKey, &m.Repo, &m.RepoID, &m.Number, &m.Title, &m.Body, &m.SourceBranch, &m.TargetBranch, &m.Status, &m.Draft,
			&aid, &aun, &adn, &ak, &m.ChannelID, &m.HeadSHA, &m.BaseSHA, &m.MergeCommitSHA,
			&mid, &mun, &mdn, &mk, &m.MergedAt, &m.ClosedAt, &m.CreatedAt, &m.UpdatedAt)
		m.Author = scanAuthor(aid, aun, adn, ak)
		m.MergedBy = scanAuthor(mid, mun, mdn, mk)
		return m, err
	})
	if out == nil {
		out = []MergeRequest{}
	}
	return out, err
}

func (s *Server) loadMergeRequests(ctx context.Context, where string, args ...any) ([]MergeRequest, error) {
	rows, err := s.DB.Query(ctx, `select `+mrCols+mrFrom+where, args...)
	if err != nil {
		return nil, err
	}
	return scanMergeRequests(rows)
}

func (s *Server) mergeRequestByNumber(ctx context.Context, app string, number int) (MergeRequest, error) {
	mrs, err := s.loadMergeRequests(ctx, `where mr.app_key = $1 and mr.number = $2`, app, number)
	if err != nil {
		return MergeRequest{}, err
	}
	if len(mrs) == 0 {
		return MergeRequest{}, errNotFound
	}
	return mrs[0], nil
}

// mrParam loads the merge request in the URL after checking the role.
func (s *Server) mrParam(r *http.Request, min access.Role) (MergeRequest, access.Role, error) {
	app := chi.URLParam(r, "app")
	role, err := s.requireRole(r, app, min)
	if err != nil {
		return MergeRequest{}, role, err
	}
	n, err := strconv.Atoi(chi.URLParam(r, "number"))
	if err != nil {
		return MergeRequest{}, role, errNotFound
	}
	mr, err := s.mergeRequestByNumber(r.Context(), app, n)
	return mr, role, err
}

var mrStatuses = map[string][]string{
	"": {"open"}, "open": {"open"}, "merged": {"merged"}, "closed": {"closed"}, "all": {"open", "merged", "closed"},
}

func (s *Server) listMergeRequests(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	if _, err := s.requireRole(r, app, access.Reader); err != nil {
		return err
	}
	statuses, ok := mrStatuses[r.URL.Query().Get("status")]
	if !ok {
		return badRequest("status must be open, merged, closed or all")
	}
	ctx := r.Context()
	mrs, err := s.loadMergeRequests(ctx, `where mr.app_key = $1 and mr.status = any($2) and ($3 = '' or r.name = $3) order by mr.number desc limit 200`,
		app, statuses, r.URL.Query().Get("repo"))
	if err != nil {
		return err
	}
	// Show each one's checks, one query per repository.
	byRepo := map[string][]string{}
	for _, m := range mrs {
		byRepo[m.RepoID] = append(byRepo[m.RepoID], m.HeadSHA)
	}
	states := map[string]map[string]string{}
	for repoID, shas := range byRepo {
		if states[repoID], err = s.commitStates(ctx, repoID, shas); err != nil {
			return err
		}
	}
	for i, m := range mrs {
		mrs[i].ChecksState = states[m.RepoID][m.HeadSHA]
	}
	writeJSON(w, http.StatusOK, mrs)
	return nil
}

// MergeStatus says whether a merge request can be merged now, and if not,
// why.
type MergeStatus struct {
	Mergeable bool     `json:"mergeable"`
	Conflicts []string `json:"conflicts"`
	// Blockers a maintainer may override: failing or unfinished checks and
	// requested changes.
	Overridable []string `json:"overridable"`
	// Reason is set when the merge request cannot be merged at all.
	Reason string `json:"reason,omitempty"`
}

// Review is one reviewer's verdict.
type Review struct {
	User      Author    `json:"user"`
	State     string    `json:"state"` // "approved" or "changes_requested"
	HeadSHA   string    `json:"head_sha"`
	Outdated  bool      `json:"outdated"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) mrReviews(ctx context.Context, mr MergeRequest) ([]Review, error) {
	rows, err := s.DB.Query(ctx, `select u.id, u.username, u.display_name, u.kind, v.state, v.head_sha, v.created_at
		from merge_request_reviews v join users u on u.id = v.user_id where v.merge_request_id = $1 order by v.created_at`, mr.ID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Review, error) {
		var v Review
		err := row.Scan(&v.User.ID, &v.User.Username, &v.User.DisplayName, &v.User.Kind, &v.State, &v.HeadSHA, &v.CreatedAt)
		v.Outdated = v.HeadSHA != mr.HeadSHA
		return v, err
	})
	if out == nil {
		out = []Review{}
	}
	return out, err
}

// mergeStatus checks an open merge request against its branches, its
// checks and its reviews.
func (s *Server) mergeStatus(ctx context.Context, mr MergeRequest, checks []Check, reviews []Review) (MergeStatus, error) {
	st := MergeStatus{Conflicts: []string{}, Overridable: []string{}}
	if mr.Status != "open" {
		st.Reason = "This merge request is " + mr.Status + "."
		return st, nil
	}
	head, err := s.Git.ResolveCommit(ctx, mr.AppKey, mr.Repo, "refs/heads/"+mr.SourceBranch)
	if err != nil {
		st.Reason = "The source branch " + mr.SourceBranch + " no longer exists."
		return st, nil
	}
	target, err := s.Git.ResolveCommit(ctx, mr.AppKey, mr.Repo, "refs/heads/"+mr.TargetBranch)
	if err != nil {
		st.Reason = "The target branch " + mr.TargetBranch + " no longer exists."
		return st, nil
	}
	if ok, err := s.Git.IsAncestor(ctx, mr.AppKey, mr.Repo, head, target); err != nil {
		return st, err
	} else if ok {
		st.Reason = mr.TargetBranch + " already contains every commit of " + mr.SourceBranch + "."
		return st, nil
	}
	_, conflicts, err := s.Git.MergeTree(ctx, mr.AppKey, mr.Repo, target, head)
	if err != nil {
		return st, err
	}
	if conflicts != nil {
		st.Conflicts = conflicts
	}
	if mr.Draft {
		st.Reason = "This merge request is a draft. Mark it ready to merge it."
	}
	switch combinedState(checks) {
	case "failure":
		st.Overridable = append(st.Overridable, "Some checks failed.")
	case "pending":
		st.Overridable = append(st.Overridable, "Some checks have not finished.")
	}
	for _, v := range reviews {
		if v.State == "changes_requested" {
			st.Overridable = append(st.Overridable, v.User.DisplayName+" requested changes.")
		}
	}
	st.Mergeable = st.Reason == "" && len(st.Conflicts) == 0
	return st, nil
}

// writeMergeRequest sends a merge request with everything its page shows.
func (s *Server) writeMergeRequest(w http.ResponseWriter, r *http.Request, mr MergeRequest, role access.Role, status int) error {
	ctx := r.Context()
	u := currentUser(r)
	links, err := s.linksFor(ctx, mr.Ref(), u.ID)
	if err != nil {
		return err
	}
	checks, err := s.commitChecks(ctx, mr.RepoID, mr.HeadSHA)
	if err != nil {
		return err
	}
	mr.ChecksState = combinedState(checks)
	reviews, err := s.mrReviews(ctx, mr)
	if err != nil {
		return err
	}
	ms, err := s.mergeStatus(ctx, mr, checks, reviews)
	if err != nil {
		return err
	}
	author := mr.Author != nil && mr.Author.ID == u.ID
	writeJSON(w, status, map[string]any{
		"merge_request": mr,
		"links":         links,
		"checks":        checks,
		"reviews":       reviews,
		"merge_status":  ms,
		"permissions": map[string]bool{
			"edit":   role >= access.Developer || author,
			"review": role >= access.Developer && !author,
			"merge":  role >= access.Developer,
			"force":  role >= access.Maintainer,
		},
	})
	return nil
}

func (s *Server) getMergeRequest(w http.ResponseWriter, r *http.Request) error {
	mr, role, err := s.mrParam(r, access.Reader)
	if err != nil {
		return err
	}
	return s.writeMergeRequest(w, r, mr, role, http.StatusOK)
}

// MergeRequestInput opens a merge request.
type MergeRequestInput struct {
	Repo         string `json:"repo"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	Draft        bool   `json:"draft"`
}

func (s *Server) createMergeRequest(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	var in MergeRequestInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	role, err := s.requireRole(r, app, access.Developer)
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `select `+repoCols+` from repositories r where r.app_key = $1 and r.name = $2`, app, in.Repo)
	if err != nil {
		return err
	}
	repos, err := scanRepos(rows)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		return badRequest("repository not found")
	}
	mr, err := s.openMergeRequest(r.Context(), currentUser(r), repos[0], in)
	if err != nil {
		return err
	}
	return s.writeMergeRequest(w, r, mr, role, http.StatusCreated)
}

// openMergeRequest creates a merge request as u, who must be allowed to.
func (s *Server) openMergeRequest(ctx context.Context, u User, repo Repo, in MergeRequestInput) (MergeRequest, error) {
	in.SourceBranch = strings.TrimSpace(in.SourceBranch)
	in.TargetBranch = strings.TrimSpace(in.TargetBranch)
	if in.TargetBranch == "" {
		in.TargetBranch = repo.DefaultBranch
	}
	if in.SourceBranch == "" {
		return MergeRequest{}, badRequest("source_branch is required")
	}
	if in.SourceBranch == in.TargetBranch {
		return MergeRequest{}, badRequest("the source and target branch must differ")
	}
	head, err := s.Git.ResolveCommit(ctx, repo.AppKey, repo.Name, "refs/heads/"+in.SourceBranch)
	if err != nil {
		return MergeRequest{}, badRequest("there is no branch " + in.SourceBranch)
	}
	target, err := s.Git.ResolveCommit(ctx, repo.AppKey, repo.Name, "refs/heads/"+in.TargetBranch)
	if err != nil {
		return MergeRequest{}, badRequest("there is no branch " + in.TargetBranch)
	}
	base, err := s.Git.MergeBase(ctx, repo.AppKey, repo.Name, target, head)
	if errors.Is(err, gitrepo.ErrNoMergeBase) {
		return MergeRequest{}, badRequest(fmt.Sprintf("%s and %s have no common history", in.SourceBranch, in.TargetBranch))
	}
	if err != nil {
		return MergeRequest{}, err
	}
	if base == head {
		return MergeRequest{}, badRequest(fmt.Sprintf("%s has no commits that %s does not have", in.SourceBranch, in.TargetBranch))
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		commits, err := s.Git.Range(ctx, repo.AppKey, repo.Name, base, head, 2)
		if err != nil {
			return MergeRequest{}, err
		}
		if len(commits) == 1 {
			in.Title = commits[0].Subject
		} else {
			in.Title = "Merge " + in.SourceBranch + " into " + in.TargetBranch
		}
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return MergeRequest{}, err
	}
	defer tx.Rollback(ctx)
	var number int
	if err := tx.QueryRow(ctx, `update applications set next_mr_number = next_mr_number + 1 where key = $1 returning next_mr_number - 1`, repo.AppKey).Scan(&number); err != nil {
		return MergeRequest{}, err
	}
	var channelID, id string
	if err := tx.QueryRow(ctx, `insert into channels (app_key, kind, name) values ($1, 'merge_request', $2) returning id`,
		repo.AppKey, fmt.Sprintf("%s!%d", repo.AppKey, number)).Scan(&channelID); err != nil {
		return MergeRequest{}, err
	}
	err = tx.QueryRow(ctx, `insert into merge_requests (app_key, repo_id, number, title, body, source_branch, target_branch, draft, author_id, channel_id, head_sha, base_sha)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) returning id`,
		repo.AppKey, repo.ID, number, in.Title, in.Body, in.SourceBranch, in.TargetBranch, in.Draft, u.ID, channelID, head, base).Scan(&id)
	if isUniqueViolation(err) {
		return MergeRequest{}, errStatus(http.StatusConflict, fmt.Sprintf("there is already an open merge request from %s into %s", in.SourceBranch, in.TargetBranch))
	}
	if err != nil {
		return MergeRequest{}, err
	}
	mr := MergeRequest{ID: id, AppKey: repo.AppKey, Repo: repo.Name, RepoID: repo.ID, Number: number, Title: in.Title, Status: "open"}
	notes, err := s.syncClosingLinks(ctx, tx, mr, in.Body, u)
	if err != nil {
		return MergeRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MergeRequest{}, err
	}
	s.keepHead(ctx, mr, head)
	s.publishNotes(ctx, notes)
	return s.afterMergeRequestChange(ctx, repo, id, "opened")
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// keepHead keeps the reviewed commits reachable after the branch is gone.
func (s *Server) keepHead(ctx context.Context, mr MergeRequest, head string) {
	if err := s.Git.KeepRef(ctx, mr.AppKey, mr.Repo, fmt.Sprintf("refs/orbit/merge-requests/%d/head", mr.Number), head); err != nil {
		slog.Error("keep merge request head", "mr", mr.reference(), "err", err)
	}
}

// afterMergeRequestChange reloads a merge request and tells the
// repository's subscribers and webhooks what happened.
func (s *Server) afterMergeRequestChange(ctx context.Context, repo Repo, id, action string) (MergeRequest, error) {
	mrs, err := s.loadMergeRequests(ctx, `where mr.id = $1`, id)
	if err != nil {
		return MergeRequest{}, err
	}
	if len(mrs) == 0 {
		return MergeRequest{}, errNotFound
	}
	mr := mrs[0]
	s.Hub.Publish("repo:"+repo.ID, "merge_request.updated", mr)
	if action != "" {
		s.deliverWebhooks(ctx, repo, "merge_request", map[string]any{"action": action, "merge_request": mr, "url": s.BaseURL + mr.url()})
	}
	return mr, nil
}

// closesRE finds "closes #4", "fixes ledger#7" or "resolves #4, #5" in a
// description.
var (
	closesRE   = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?\s+((?:[a-z0-9-]*#\d+)(?:\s*(?:,|and)\s*[a-z0-9-]*#\d+)*)`)
	issueRefRE = regexp.MustCompile(`(?i)([a-z0-9-]*)#(\d+)`)
)

const maxClosings = 20

// closedIssues lists the issues a description says it closes.
func closedIssues(app, body string) [][2]string {
	var out [][2]string
	seen := map[string]bool{}
	for _, m := range closesRE.FindAllStringSubmatch(body, -1) {
		for _, ref := range issueRefRE.FindAllStringSubmatch(m[1], -1) {
			a := strings.ToLower(ref[1])
			if a == "" {
				a = app
			}
			if key := a + "#" + ref[2]; !seen[key] && len(out) < maxClosings {
				seen[key] = true
				out = append(out, [2]string{a, ref[2]})
			}
		}
	}
	return out
}

// syncClosingLinks links the merge request to the issues its description
// closes, replacing earlier closing links, and notes new ones in the issues'
// discussions. Issues the author cannot read are ignored. It returns the
// notes to publish after the commit.
func (s *Server) syncClosingLinks(ctx context.Context, tx pgx.Tx, mr MergeRequest, body string, u User) ([]string, error) {
	var old []string
	rows, err := tx.Query(ctx, `select to_id from links where from_type = 'merge_request' and from_id = $1 and kind = $2`, mr.ID, LinkCloses)
	if err != nil {
		return nil, err
	}
	if old, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `delete from links where from_type = 'merge_request' and from_id = $1 and kind = $2`, mr.ID, LinkCloses); err != nil {
		return nil, err
	}
	var notes []string
	mrEntity := Entity{Ref: mr.Ref(), Title: mr.Title, Subtitle: mr.reference() + " · " + mr.Status, URL: mr.url(), App: mr.AppKey}
	for _, ref := range closedIssues(mr.AppKey, body) {
		role, err := s.Access.AppRole(ctx, u.ID, ref[0])
		if err != nil {
			return nil, err
		}
		if role < access.Reader {
			continue
		}
		var issueID, channelID string
		err = tx.QueryRow(ctx, `select id, channel_id from issues where app_key = $1 and number = $2`, ref[0], ref[1]).Scan(&issueID, &channelID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := addLink(ctx, tx, mr.Ref(), Ref{Type: "issue", ID: issueID}, LinkCloses, nil, u.ID); err != nil {
			return nil, err
		}
		if contains(old, issueID) {
			continue
		}
		id, err := s.postSystemMessage(ctx, tx, channelID, nil,
			fmt.Sprintf("%s mentioned this in merge request %s, which closes it when merged", u.DisplayName, mr.reference()),
			[]any{map[string]any{"type": "entity", "entity": mrEntity}}, nil)
		if err != nil {
			return nil, err
		}
		notes = append(notes, id)
	}
	return notes, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// publishNotes sends messages written in a transaction once it committed.
func (s *Server) publishNotes(ctx context.Context, ids []string) {
	for _, id := range ids {
		if m, err := s.messageByID(ctx, id, ""); err == nil {
			s.publishMessage(ctx, m)
		}
	}
}

func (s *Server) updateMergeRequest(w http.ResponseWriter, r *http.Request) error {
	mr, role, err := s.mrParam(r, access.Reader)
	if err != nil {
		return err
	}
	var in struct {
		Title  *string `json:"title"`
		Body   *string `json:"body"`
		Draft  *bool   `json:"draft"`
		Status *string `json:"status"` // "closed" or "open"
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u := currentUser(r)
	if role < access.Developer && (mr.Author == nil || mr.Author.ID != u.ID) {
		return errForbidden
	}
	if mr.Status == "merged" && (in.Status != nil || in.Draft != nil) {
		return errStatus(http.StatusConflict, "this merge request is already merged")
	}
	ctx := r.Context()
	repo, err := s.repoByID(ctx, mr.RepoID)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var events, notes []string
	action := ""
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return badRequest("title is required")
		}
		if _, err := tx.Exec(ctx, `update merge_requests set title = $2, updated_at = now() where id = $1`, mr.ID, t); err != nil {
			return err
		}
		mr.Title = t
	}
	if in.Body != nil {
		if _, err := tx.Exec(ctx, `update merge_requests set body = $2, updated_at = now() where id = $1`, mr.ID, *in.Body); err != nil {
			return err
		}
		if notes, err = s.syncClosingLinks(ctx, tx, mr, *in.Body, u); err != nil {
			return err
		}
	}
	if in.Draft != nil && *in.Draft != mr.Draft {
		if _, err := tx.Exec(ctx, `update merge_requests set draft = $2, updated_at = now() where id = $1`, mr.ID, *in.Draft); err != nil {
			return err
		}
		if *in.Draft {
			events = append(events, u.DisplayName+" marked this as a draft")
		} else {
			events = append(events, u.DisplayName+" marked this as ready")
		}
		action = "updated"
	}
	if in.Status != nil && *in.Status != mr.Status {
		switch *in.Status {
		case "closed":
			if _, err := tx.Exec(ctx, `update merge_requests set status = 'closed', closed_at = now(), updated_at = now() where id = $1`, mr.ID); err != nil {
				return err
			}
			events = append(events, u.DisplayName+" closed this")
			action = "closed"
		case "open":
			head, err := s.Git.ResolveCommit(ctx, mr.AppKey, mr.Repo, "refs/heads/"+mr.SourceBranch)
			if err != nil {
				return errStatus(http.StatusConflict, "the source branch "+mr.SourceBranch+" no longer exists")
			}
			base, err := s.mergeBaseFor(ctx, mr, head)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `update merge_requests set status = 'open', closed_at = null, head_sha = $2, base_sha = $3, updated_at = now() where id = $1`, mr.ID, head, base)
			if isUniqueViolation(err) {
				return errStatus(http.StatusConflict, fmt.Sprintf("there is already an open merge request from %s into %s", mr.SourceBranch, mr.TargetBranch))
			}
			if err != nil {
				return err
			}
			events = append(events, u.DisplayName+" reopened this")
			action = "reopened"
		default:
			return badRequest("status must be open or closed")
		}
	}
	for _, e := range events {
		id, err := s.postSystemMessage(ctx, tx, mr.ChannelID, nil, e, []any{}, nil)
		if err != nil {
			return err
		}
		notes = append(notes, id)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.publishNotes(ctx, notes)
	if action == "" && (in.Title != nil || in.Body != nil) {
		action = "updated"
	}
	mr, err = s.afterMergeRequestChange(ctx, repo, mr.ID, action)
	if err != nil {
		return err
	}
	return s.writeMergeRequest(w, r, mr, role, http.StatusOK)
}

// mergeBaseFor is the merge base of head with the target branch's tip.
func (s *Server) mergeBaseFor(ctx context.Context, mr MergeRequest, head string) (string, error) {
	target, err := s.Git.ResolveCommit(ctx, mr.AppKey, mr.Repo, "refs/heads/"+mr.TargetBranch)
	if err != nil {
		return mr.BaseSHA, nil // the target is gone: keep the old base
	}
	base, err := s.Git.MergeBase(ctx, mr.AppKey, mr.Repo, target, head)
	if errors.Is(err, gitrepo.ErrNoMergeBase) {
		return mr.BaseSHA, nil
	}
	return base, err
}

func (s *Server) mergeRequestDiff(w http.ResponseWriter, r *http.Request) error {
	mr, _, err := s.mrParam(r, access.Reader)
	if err != nil {
		return err
	}
	files, err := s.Git.Diff(r.Context(), mr.AppKey, mr.Repo, mr.BaseSHA, mr.HeadSHA)
	if err != nil {
		return gitErr(err)
	}
	moved, err := s.movedComments(r.Context(), mr, currentUser(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"base": mr.BaseSHA, "head": mr.HeadSHA, "files": files, "moved": moved})
	return nil
}

// MovedComment is where a comment on an earlier version of a merge request
// sits now, because the lines it discusses did not change since.
type MovedComment struct {
	MessageID string `json:"message_id"`
	Side      string `json:"side"` // "new" (at the head) or "old" (at the base)
	Path      string `json:"path"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
}

// maxMovedDiffs bounds the git diffs one request runs to follow comments.
const maxMovedDiffs = 50

// movedComments follows review comments made on earlier versions to the
// current head (or, for comments on an earlier base, the current base).
func (s *Server) movedComments(ctx context.Context, mr MergeRequest, viewer string) ([]MovedComment, error) {
	rows, err := s.DB.Query(ctx, `select m.id, a->'code'->>'commit', a->'code'->>'path', (a->'code'->>'start')::int, (a->'code'->>'end')::int
		from messages m, jsonb_array_elements(m.attachments) a
		where m.channel_id = $1 and m.thread_id is null and a->>'type' = 'code' and `+visibleTo("$2")+`
		  and a->'code'->>'commit' not in ($3, $4) order by m.created_at`, mr.ChannelID, viewer, mr.HeadSHA, mr.BaseSHA)
	if err != nil {
		return nil, err
	}
	type anchor struct {
		id, commit, path string
		start, end       int
	}
	anchors, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (anchor, error) {
		var a anchor
		err := row.Scan(&a.id, &a.commit, &a.path, &a.start, &a.end)
		return a, err
	})
	if err != nil {
		return nil, err
	}
	out := []MovedComment{}
	mappers := map[string]func(int) (int, bool){}
	for _, a := range anchors {
		// A comment on a former base sits on the old side, others on the
		// new side.
		side, to := "new", mr.HeadSHA
		if ok, err := s.Git.IsAncestor(ctx, mr.AppKey, mr.Repo, a.commit, mr.BaseSHA); err == nil && ok {
			side, to = "old", mr.BaseSHA
		}
		k := a.commit + "\x00" + a.path + "\x00" + to
		m, seen := mappers[k]
		if !seen {
			if len(mappers) >= maxMovedDiffs {
				continue
			}
			f, changed, err := s.Git.DiffPath(ctx, mr.AppKey, mr.Repo, a.commit, to, a.path)
			switch {
			case err != nil:
				m = nil // the commit is gone
			case !changed:
				m = func(n int) (int, bool) { return n, true }
			case f.Status == "deleted" || f.Binary:
				m = nil
			default:
				m = gitrepo.LineMapper(f.Hunks)
			}
			mappers[k] = m
		}
		if m == nil {
			continue
		}
		start, ok1 := m(a.start)
		end, ok2 := m(a.end)
		if ok1 && ok2 && end-start == a.end-a.start {
			out = append(out, MovedComment{MessageID: a.id, Side: side, Path: a.path, Start: start, End: end})
		}
	}
	return out, nil
}

func (s *Server) mergeRequestCommits(w http.ResponseWriter, r *http.Request) error {
	mr, _, err := s.mrParam(r, access.Reader)
	if err != nil {
		return err
	}
	repo, err := s.repoByID(r.Context(), mr.RepoID)
	if err != nil {
		return err
	}
	commits, err := s.Git.Range(r.Context(), mr.AppKey, mr.Repo, mr.BaseSHA, mr.HeadSHA, 250)
	if err != nil {
		return gitErr(err)
	}
	out, err := s.commitsWithState(r.Context(), repo, commits)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) reviewMergeRequest(w http.ResponseWriter, r *http.Request) error {
	mr, role, err := s.mrParam(r, access.Developer)
	if err != nil {
		return err
	}
	var in struct {
		State string `json:"state"` // "approved", "changes_requested" or "" to withdraw
		Body  string `json:"body"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u := currentUser(r)
	if mr.Author != nil && mr.Author.ID == u.ID {
		return errStatus(http.StatusForbidden, "you cannot review your own merge request")
	}
	if mr.Status != "open" {
		return errStatus(http.StatusConflict, "this merge request is "+mr.Status)
	}
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var event string
	switch in.State {
	case "approved", "changes_requested":
		if _, err := tx.Exec(ctx, `insert into merge_request_reviews (merge_request_id, user_id, state, head_sha) values ($1, $2, $3, $4)
			on conflict (merge_request_id, user_id) do update set state = excluded.state, head_sha = excluded.head_sha, created_at = now()`,
			mr.ID, u.ID, in.State, mr.HeadSHA); err != nil {
			return err
		}
		event = u.DisplayName + " approved this"
		if in.State == "changes_requested" {
			event = u.DisplayName + " requested changes"
		}
	case "":
		if _, err := tx.Exec(ctx, `delete from merge_request_reviews where merge_request_id = $1 and user_id = $2`, mr.ID, u.ID); err != nil {
			return err
		}
		event = u.DisplayName + " withdrew their review"
	default:
		return badRequest("state must be approved, changes_requested or empty")
	}
	if b := strings.TrimSpace(in.Body); b != "" {
		event += ": " + b
	}
	note, err := s.postSystemMessage(ctx, tx, mr.ChannelID, nil, event, []any{}, nil)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update merge_requests set updated_at = now() where id = $1`, mr.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.publishNotes(ctx, []string{note})
	repo, err := s.repoByID(ctx, mr.RepoID)
	if err != nil {
		return err
	}
	if mr, err = s.afterMergeRequestChange(ctx, repo, mr.ID, "reviewed"); err != nil {
		return err
	}
	return s.writeMergeRequest(w, r, mr, role, http.StatusOK)
}

func (s *Server) mergeMergeRequest(w http.ResponseWriter, r *http.Request) error {
	mr, role, err := s.mrParam(r, access.Developer)
	if err != nil {
		return err
	}
	var in struct {
		Strategy           string `json:"strategy"` // "merge" (default) or "squash"
		DeleteSourceBranch bool   `json:"delete_source_branch"`
		// Force merges despite failing checks or requested changes. It
		// needs the maintainer role.
		Force bool `json:"force"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Strategy == "" {
		in.Strategy = "merge"
	}
	if in.Strategy != "merge" && in.Strategy != "squash" {
		return badRequest("strategy must be merge or squash")
	}
	if in.Force && role < access.Maintainer {
		return errStatus(http.StatusForbidden, "merging despite failing checks or requested changes needs the maintainer role")
	}
	ctx := r.Context()
	checks, err := s.commitChecks(ctx, mr.RepoID, mr.HeadSHA)
	if err != nil {
		return err
	}
	reviews, err := s.mrReviews(ctx, mr)
	if err != nil {
		return err
	}
	st, err := s.mergeStatus(ctx, mr, checks, reviews)
	if err != nil {
		return err
	}
	switch {
	case st.Reason != "":
		return errStatus(http.StatusConflict, st.Reason)
	case len(st.Conflicts) > 0:
		return errStatus(http.StatusConflict, "the branches conflict in "+strings.Join(st.Conflicts, ", "))
	case len(st.Overridable) > 0 && !in.Force:
		return errStatus(http.StatusConflict, strings.Join(st.Overridable, " ")+" A maintainer can merge anyway.")
	}
	u := currentUser(r)
	repo, err := s.repoByID(ctx, mr.RepoID)
	if err != nil {
		return err
	}
	head, err := s.Git.ResolveCommit(ctx, mr.AppKey, mr.Repo, "refs/heads/"+mr.SourceBranch)
	if err != nil || head != mr.HeadSHA {
		return errStatus(http.StatusConflict, "the source branch changed; reload and try again")
	}
	target, err := s.Git.ResolveCommit(ctx, mr.AppKey, mr.Repo, "refs/heads/"+mr.TargetBranch)
	if err != nil {
		return errStatus(http.StatusConflict, "the target branch no longer exists")
	}
	tree, conflicts, err := s.Git.MergeTree(ctx, mr.AppKey, mr.Repo, target, head)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return errStatus(http.StatusConflict, "the branches conflict in "+strings.Join(conflicts, ", "))
	}
	me := gitrepo.Person{Name: u.DisplayName, Email: u.Username + "@users.orbit.invalid"}
	var commit string
	if in.Strategy == "squash" {
		msg := fmt.Sprintf("%s (%s)\n\n%s\n", mr.Title, mr.reference(), strings.TrimSpace(mr.Body))
		author := me
		if mr.Author != nil && mr.Author.Kind == "human" {
			author = gitrepo.Person{Name: mr.Author.DisplayName, Email: mr.Author.Username + "@users.orbit.invalid"}
		}
		commit, err = s.Git.CommitTree(ctx, mr.AppKey, mr.Repo, tree, msg, author, me, target)
	} else {
		msg := fmt.Sprintf("Merge branch '%s' into '%s'\n\n%s\n\nMerge request %s\n", mr.SourceBranch, mr.TargetBranch, mr.Title, mr.reference())
		commit, err = s.Git.CommitTree(ctx, mr.AppKey, mr.Repo, tree, msg, me, me, target, head)
	}
	if err != nil {
		return err
	}
	if err := s.Git.MoveBranch(ctx, mr.AppKey, mr.Repo, mr.TargetBranch, commit, target); err != nil {
		if errors.Is(err, gitrepo.ErrRefMoved) {
			return errStatus(http.StatusConflict, mr.TargetBranch+" changed while merging; try again")
		}
		return err
	}
	tag, err := s.DB.Exec(ctx, `update merge_requests set status = 'merged', merge_commit_sha = $2, merged_by = $3, merged_at = now(), updated_at = now()
		where id = $1 and status = 'open'`, mr.ID, commit, u.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errStatus(http.StatusConflict, "this merge request changed while merging")
	}
	mr.Status = "merged"
	notes := []string{}
	note := fmt.Sprintf("%s merged this into %s", u.DisplayName, mr.TargetBranch)
	if in.Force && len(st.Overridable) > 0 {
		note += " despite: " + strings.Join(st.Overridable, " ")
	}
	if id, err := s.postSystemMessage(ctx, s.DB, mr.ChannelID, nil, note, []any{}, nil); err == nil {
		notes = append(notes, id)
	}
	closed, err := s.closeLinkedIssues(ctx, mr, u)
	if err != nil {
		slog.Error("close issues of merge request", "mr", mr.reference(), "err", err)
	}
	notes = append(notes, closed...)
	if in.DeleteSourceBranch {
		if err := s.Git.DeleteBranch(ctx, mr.AppKey, mr.Repo, mr.SourceBranch, head); err == nil {
			if id, err := s.postSystemMessage(ctx, s.DB, mr.ChannelID, nil, "Deleted the branch "+mr.SourceBranch, []any{}, nil); err == nil {
				notes = append(notes, id)
			}
		}
	}
	s.publishNotes(ctx, notes)
	if mr, err = s.afterMergeRequestChange(ctx, repo, mr.ID, "merged"); err != nil {
		return err
	}
	// The target moved: run its pipeline and tell webhooks, as for a push.
	s.onPush(context.WithoutCancel(ctx), repo, u, mr.TargetBranch, target, commit)
	return s.writeMergeRequest(w, r, mr, role, http.StatusOK)
}

// closeLinkedIssues marks the issues a merged merge request closes as done,
// in the applications where whoever merged it may change issues.
func (s *Server) closeLinkedIssues(ctx context.Context, mr MergeRequest, by User) ([]string, error) {
	rows, err := s.DB.Query(ctx, `
		select i.id, i.app_key, i.channel_id from issues i join links l on l.to_type = 'issue' and l.to_id = i.id::text
		 where l.from_type = 'merge_request' and l.from_id = $1 and l.kind = $2 and i.status in ('open', 'in_progress')`, mr.ID, LinkCloses)
	if err != nil {
		return nil, err
	}
	type target struct{ id, app, channel string }
	targets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (target, error) {
		var t target
		err := row.Scan(&t.id, &t.app, &t.channel)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	var channels []string
	for _, t := range targets {
		if role, err := s.Access.AppRole(ctx, by.ID, t.app); err != nil || role < access.Developer {
			continue
		}
		if _, err := s.DB.Exec(ctx, `update issues set status = 'done', updated_at = now() where id = $1`, t.id); err != nil {
			return nil, err
		}
		channels = append(channels, t.channel)
	}
	e, err := s.resolve(ctx, mr.Ref())
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, ch := range channels {
		id, err := s.postSystemMessage(ctx, s.DB, ch, nil, fmt.Sprintf("%s closed this by merging %s", by.DisplayName, mr.reference()),
			[]any{map[string]any{"type": "entity", "entity": e}}, nil)
		if err != nil {
			return notes, err
		}
		notes = append(notes, id)
	}
	return notes, nil
}

// syncMergeRequests follows a push: open merge requests from the branch get
// the new tip, and ones into it are marked merged once it contains their
// tip. It runs before the push's pipeline starts.
func (s *Server) syncMergeRequests(ctx context.Context, repo Repo, pusher User, branch, after string) {
	mrs, err := s.loadMergeRequests(ctx, `where mr.repo_id = $1 and mr.status = 'open' and (mr.source_branch = $2 or mr.target_branch = $2)`, repo.ID, branch)
	if err != nil {
		slog.Error("load merge requests for push", "err", err)
		return
	}
	for _, mr := range mrs {
		var note, action string
		if mr.SourceBranch == branch {
			if mr.HeadSHA == after {
				continue
			}
			base, err := s.mergeBaseFor(ctx, mr, after)
			if err != nil {
				slog.Error("merge base", "mr", mr.reference(), "err", err)
				continue
			}
			n := 0
			if commits, err := s.Git.Range(ctx, mr.AppKey, mr.Repo, mr.HeadSHA, after, 100); err == nil {
				n = len(commits)
			}
			if _, err := s.DB.Exec(ctx, `update merge_requests set head_sha = $2, base_sha = $3, updated_at = now() where id = $1`, mr.ID, after, base); err != nil {
				slog.Error("update merge request head", "err", err)
				continue
			}
			s.keepHead(ctx, mr, after)
			if n > 0 {
				note = fmt.Sprintf("%s pushed %d %s", pusher.DisplayName, n, plural(n, "commit", "commits"))
			} else {
				note = fmt.Sprintf("%s rewrote %s (now at %s)", pusher.DisplayName, mr.SourceBranch, shortSHA(after))
			}
			action = "updated"
		} else {
			merged, err := s.Git.IsAncestor(ctx, mr.AppKey, mr.Repo, mr.HeadSHA, after)
			if err != nil {
				continue
			}
			if !merged {
				// The merge base can move when the target takes some of the
				// source's commits.
				if base, err := s.mergeBaseFor(ctx, mr, mr.HeadSHA); err == nil && base != mr.BaseSHA {
					s.DB.Exec(ctx, `update merge_requests set base_sha = $2, updated_at = now() where id = $1`, mr.ID, base)
					action = "updated"
				}
			} else {
				if _, err := s.DB.Exec(ctx, `update merge_requests set status = 'merged', merge_commit_sha = $2, merged_by = $3, merged_at = now(), updated_at = now()
					where id = $1`, mr.ID, after, pusher.ID); err != nil {
					continue
				}
				note = fmt.Sprintf("%s merged this by pushing to %s", pusher.DisplayName, mr.TargetBranch)
				action = "merged"
				mr.Status = "merged"
				if ids, err := s.closeLinkedIssues(ctx, mr, pusher); err == nil {
					s.publishNotes(ctx, ids)
				}
			}
		}
		if note != "" {
			if id, err := s.postSystemMessage(ctx, s.DB, mr.ChannelID, nil, note, []any{}, nil); err == nil {
				s.publishNotes(ctx, []string{id})
			}
		}
		if action != "" {
			s.afterMergeRequestChange(ctx, repo, mr.ID, action)
		}
	}
}

// mergeRequestForChannel finds the merge request a discussion belongs to.
func (s *Server) mergeRequestForChannel(ctx context.Context, ch Channel) (MergeRequest, error) {
	if ch.Kind != "merge_request" {
		return MergeRequest{}, errNotFound
	}
	mrs, err := s.loadMergeRequests(ctx, `where mr.channel_id = $1`, ch.ID)
	if err != nil {
		return MergeRequest{}, err
	}
	if len(mrs) == 0 {
		return MergeRequest{}, errNotFound
	}
	return mrs[0], nil
}
