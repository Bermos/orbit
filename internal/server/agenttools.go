package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Bermos/orbit/internal/access"
	"github.com/Bermos/orbit/internal/agent"
)

// Orbit's tools for agents: a small, typed set over the public API. Every
// call runs in-process as the asking user, so the API's own permission
// checks apply, and on top of that the toolbox enforces the rest of the rule:
//
//	effective role = min(asker's role, agent's role and scope, audience)
//
// where the audience is everyone who can read the conversation (only in a
// shared channel). Personal data (to-dos, private messages) only reaches the
// model when the question was asked privately.

type toolbox struct {
	s       *Server
	def     agent.Definition
	asker   User
	channel Channel
	private bool
	runID   string
	trigger Message
	roles   map[string]access.Role
}

type tool struct {
	spec agent.ToolSpec
	// personal tools read the asker's own data and exist only in private runs.
	personal bool
	run      func(t *toolbox, ctx context.Context, in json.RawMessage) (any, error)
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func schema(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

var tools = []tool{
	{spec: agent.ToolSpec{Name: "search", Description: "Full-text search over applications, issues and messages (chat and issue discussions). Returns entities with a snippet. Use it first to find what a question is about.",
		Schema: schema([]string{"query"}, map[string]any{
			"query": str("Search words. Supports quotes for phrases, OR, and -word to exclude."),
			"app":   str("Optional application key to search in."),
		})}, run: (*toolbox).search},
	{spec: agent.ToolSpec{Name: "list_issues", Description: "List an application's issues.",
		Schema: schema([]string{"app"}, map[string]any{
			"app":    str("Application key, for example ledger."),
			"status": str("active (default: open and in progress), finished, all, open, in_progress, done or closed."),
		})}, run: (*toolbox).listIssues},
	{spec: agent.ToolSpec{Name: "get_issue", Description: "Read an issue: its fields, its links and the latest messages of its discussion.",
		Schema: schema([]string{"app", "number"}, map[string]any{
			"app":    str("Application key."),
			"number": num("Issue number."),
		})}, run: (*toolbox).getIssue},
	{spec: agent.ToolSpec{Name: "get_thread", Description: "Read a message with its whole thread and the thread's links (issues, to-dos and code it is connected to).",
		Schema: schema([]string{"message_id"}, map[string]any{
			"message_id": str("ID of any message in the thread."),
		})}, run: (*toolbox).getThread},
	{spec: agent.ToolSpec{Name: "get_application", Description: "Read an application: owner team, responsible teams, repositories and channels.",
		Schema: schema([]string{"app"}, map[string]any{
			"app": str("Application key."),
		})}, run: (*toolbox).getApplication},
	{spec: agent.ToolSpec{Name: "list_files", Description: "List a directory of a repository at a branch, tag or commit.",
		Schema: schema([]string{"app", "repo"}, map[string]any{
			"app":  str("Application key."),
			"repo": str("Repository name."),
			"ref":  str("Branch, tag or commit. Defaults to the default branch."),
			"path": str("Directory path. Defaults to the root."),
		})}, run: (*toolbox).listFiles},
	{spec: agent.ToolSpec{Name: "read_file", Description: "Read a file from a repository, with line numbers. Reads up to 400 lines at a time.",
		Schema: schema([]string{"app", "repo", "path"}, map[string]any{
			"app":        str("Application key."),
			"repo":       str("Repository name."),
			"path":       str("File path."),
			"ref":        str("Branch, tag or commit. Defaults to the default branch."),
			"start_line": num("First line to read (default 1)."),
			"end_line":   num("Last line to read."),
		})}, run: (*toolbox).readFile},
	{spec: agent.ToolSpec{Name: "list_my_todos", Description: "List the asking person's to-dos.",
		Schema: schema(nil, map[string]any{})}, personal: true, run: (*toolbox).listMyTodos},
	{spec: agent.ToolSpec{Name: "propose_issue", Description: "Draft a new issue. The person sees it with a button and creates it with one click; nothing is created until then. The issue links back to this conversation.",
		Schema: schema([]string{"title"}, map[string]any{
			"app":   str("Application key. Defaults to the application of this conversation."),
			"title": str("Short issue title."),
			"body":  str("Issue description in plain text or light Markdown."),
		})}, run: (*toolbox).proposeIssue},
	{spec: agent.ToolSpec{Name: "propose_todo", Description: "Draft a to-do for the asking person, linked to this conversation. They add it with one click.",
		Schema: schema([]string{"title"}, map[string]any{
			"title": str("What to do."),
		})}, run: (*toolbox).proposeTodo},
	{spec: agent.ToolSpec{Name: "propose_issue_comment", Description: "Draft a comment on an issue's discussion, posted as the asking person once they confirm.",
		Schema: schema([]string{"app", "number", "body"}, map[string]any{
			"app":    str("Application key."),
			"number": num("Issue number."),
			"body":   str("The comment."),
		})}, run: (*toolbox).proposeIssueComment},
}

func toolNames() []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.spec.Name
	}
	return names
}

func (t *toolbox) available(tl tool) bool {
	return t.def.AllowsTool(tl.spec.Name) && (!tl.personal || t.private)
}

func (t *toolbox) Specs() []agent.ToolSpec {
	var specs []agent.ToolSpec
	for _, tl := range tools {
		if t.available(tl) {
			specs = append(specs, tl.spec)
		}
	}
	return specs
}

// maxToolResult keeps one tool result from flooding the model's context.
const maxToolResult = 24000

func (t *toolbox) Call(ctx context.Context, name string, input json.RawMessage) (string, error) {
	i := slices.IndexFunc(tools, func(tl tool) bool { return tl.spec.Name == name })
	if i < 0 || !t.available(tools[i]) {
		return "", fmt.Errorf("tool %q is not available here", name)
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	out, err := tools[i].run(t, ctx, input)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	if len(raw) > maxToolResult {
		return string(raw[:maxToolResult]) + "\n[truncated: narrow the request to see more]", nil
	}
	return string(raw), nil
}

func decode[T any](in json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(in, &v); err != nil {
		return v, fmt.Errorf("invalid input: %w", err)
	}
	return v, nil
}

// role is the effective role for an application, the heart of the rule above.
func (t *toolbox) role(ctx context.Context, app string) (access.Role, error) {
	if r, ok := t.roles[app]; ok {
		return r, nil
	}
	user, err := t.s.Access.AppRole(ctx, t.asker.ID, app)
	if err != nil {
		return access.None, err
	}
	agentRole := access.None
	if t.def.AllowsApp(app) {
		agentRole = access.ParseRole(t.def.Role)
	}
	audience := access.Owner
	if !t.private {
		audience = access.Audience
	}
	r := access.Min(user, agentRole, audience)
	if t.roles == nil {
		t.roles = map[string]access.Role{}
	}
	t.roles[app] = r
	return r, nil
}

func (t *toolbox) requireApp(ctx context.Context, app string) error {
	if app == "" {
		return errors.New("app is required")
	}
	if !t.def.AllowsApp(app) {
		return fmt.Errorf("@%s is not allowed to use application %q", t.def.Name, app)
	}
	r, err := t.role(ctx, app)
	if err != nil {
		return err
	}
	if r < access.Reader {
		return fmt.Errorf("not allowed to read application %q here", app)
	}
	return nil
}

// visible reports whether an entity may appear in this run's answer.
func (t *toolbox) visible(ctx context.Context, e Entity) bool {
	if e.Missing || (e.Private && !t.private) || !e.visibleFor(t.asker.ID) {
		return false
	}
	if e.Type == "todo" && !t.private {
		return false
	}
	if e.App == "" {
		return true
	}
	return t.requireApp(ctx, e.App) == nil
}

func (t *toolbox) api(ctx context.Context, method, path string, in, out any) error {
	return t.s.callAPI(ctx, t.asker, method, path, in, out)
}

// compact forms keep tool results small and free of noise.

type msgOut struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	Replies   int    `json:"replies,omitempty"`
}

func (t *toolbox) messages(ms []Message) []msgOut {
	out := []msgOut{}
	for _, m := range ms {
		if m.Private && !t.private {
			continue
		}
		if m.Body == "" && len(m.Attachments) <= 2 {
			continue
		}
		author := "Orbit"
		if m.Author != nil {
			author = "@" + m.Author.Username
		}
		body := truncate(m.Body, 3000)
		var atts []map[string]any
		json.Unmarshal(m.Attachments, &atts)
		for _, a := range atts {
			if a["type"] == "code" {
				if c, ok := a["code"].(map[string]any); ok {
					body += fmt.Sprintf("\n[discusses %v/%v %v lines %v-%v at %v]", c["app"], c["repo"], c["path"], c["start"], c["end"], c["commit"])
				}
			}
		}
		out = append(out, msgOut{ID: m.ID, Author: author, Body: body, CreatedAt: m.CreatedAt.UTC().Format("2006-01-02 15:04"), Replies: m.ReplyCount})
	}
	return out
}

type entityOut struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	App      string `json:"app,omitempty"`
}

func (t *toolbox) links(ctx context.Context, ls []LinkView) []map[string]any {
	out := []map[string]any{}
	for _, l := range ls {
		if !t.visible(ctx, l.Entity) {
			continue
		}
		e := l.Entity
		out = append(out, map[string]any{"kind": l.Kind, "direction": l.Direction,
			"entity": entityOut{Type: e.Type, ID: e.ID, Title: e.Title, Subtitle: e.Subtitle, App: e.App}})
	}
	return out
}

func (t *toolbox) search(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[struct {
		Query string `json:"query"`
		App   string `json:"app"`
	}](in)
	if err != nil {
		return nil, err
	}
	q := url.Values{"q": {args.Query}}
	if args.App != "" {
		if err := t.requireApp(ctx, args.App); err != nil {
			return nil, err
		}
		q.Set("app", args.App)
	}
	var results []SearchResult
	if err := t.api(ctx, "GET", "/search?"+q.Encode(), nil, &results); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range results {
		// The question itself is already in the conversation.
		if r.Entity.Type == "message" && r.Entity.ID == t.trigger.ID {
			continue
		}
		if !t.visible(ctx, r.Entity) {
			continue
		}
		e := r.Entity
		item := map[string]any{"type": e.Type, "id": e.ID, "title": e.Title, "subtitle": e.Subtitle, "snippet": r.Snippet}
		if e.App != "" {
			item["app"] = e.App
		}
		if e.Type == "issue" {
			if n, err := issueNumberFromSubtitle(e.Subtitle); err == nil {
				item["number"] = n
			}
		}
		out = append(out, item)
	}
	return map[string]any{"results": out}, nil
}

// issueNumberFromSubtitle reads "ledger#12 · open".
func issueNumberFromSubtitle(sub string) (int, error) {
	_, rest, _ := strings.Cut(sub, "#")
	num, _, _ := strings.Cut(rest, " ")
	return strconv.Atoi(num)
}

func (t *toolbox) listIssues(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[struct {
		App    string `json:"app"`
		Status string `json:"status"`
	}](in)
	if err != nil {
		return nil, err
	}
	if err := t.requireApp(ctx, args.App); err != nil {
		return nil, err
	}
	var issues []Issue
	if err := t.api(ctx, "GET", "/apps/"+url.PathEscape(args.App)+"/issues?"+url.Values{"status": {args.Status}}.Encode(), nil, &issues); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, i := range issues {
		item := map[string]any{"number": i.Number, "title": i.Title, "status": i.Status, "updated_at": i.UpdatedAt.UTC().Format("2006-01-02")}
		if i.Assignee != nil {
			item["assignee"] = "@" + i.Assignee.Username
		}
		out = append(out, item)
	}
	return map[string]any{"app": args.App, "issues": out}, nil
}

func (t *toolbox) getIssue(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[struct {
		App    string `json:"app"`
		Number int    `json:"number"`
	}](in)
	if err != nil {
		return nil, err
	}
	if err := t.requireApp(ctx, args.App); err != nil {
		return nil, err
	}
	var res struct {
		Issue Issue      `json:"issue"`
		Links []LinkView `json:"links"`
	}
	if err := t.api(ctx, "GET", fmt.Sprintf("/apps/%s/issues/%d", url.PathEscape(args.App), args.Number), nil, &res); err != nil {
		return nil, err
	}
	var discussion []Message
	if err := t.api(ctx, "GET", "/channels/"+res.Issue.ChannelID+"/messages", nil, &discussion); err != nil {
		return nil, err
	}
	if len(discussion) > 40 {
		discussion = discussion[len(discussion)-40:]
	}
	i := res.Issue
	issue := map[string]any{"app": i.AppKey, "number": i.Number, "title": i.Title, "body": truncate(i.Body, 6000), "status": i.Status,
		"created_at": i.CreatedAt.UTC().Format("2006-01-02"), "updated_at": i.UpdatedAt.UTC().Format("2006-01-02")}
	if i.Author != nil {
		issue["author"] = "@" + i.Author.Username
	}
	if i.Assignee != nil {
		issue["assignee"] = "@" + i.Assignee.Username
	}
	return map[string]any{"issue": issue, "links": t.links(ctx, res.Links), "discussion": t.messages(discussion)}, nil
}

func (t *toolbox) getThread(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[struct {
		MessageID string `json:"message_id"`
	}](in)
	if err != nil {
		return nil, err
	}
	var m Message
	if err := t.api(ctx, "GET", "/messages/"+url.PathEscape(args.MessageID), nil, &m); err != nil {
		return nil, err
	}
	if m.Private && !t.private {
		return nil, errors.New("message not found")
	}
	var ch struct {
		Channel Channel `json:"channel"`
	}
	if err := t.api(ctx, "GET", "/channels/"+m.ChannelID, nil, &ch); err != nil {
		return nil, err
	}
	if ch.Channel.AppKey != nil {
		if err := t.requireApp(ctx, *ch.Channel.AppKey); err != nil {
			return nil, err
		}
	}
	root := m.ID
	if m.ThreadID != nil {
		root = *m.ThreadID
	}
	var thread []Message
	if err := t.api(ctx, "GET", "/channels/"+m.ChannelID+"/messages?thread="+url.QueryEscape(root), nil, &thread); err != nil {
		return nil, err
	}
	var links []LinkView
	if err := t.api(ctx, "GET", "/links?"+url.Values{"type": {"message"}, "id": {root}}.Encode(), nil, &links); err != nil {
		return nil, err
	}
	where := "#" + ch.Channel.Name
	if ch.Channel.AppKey != nil {
		where = *ch.Channel.AppKey + " " + where
	}
	return map[string]any{"channel": where, "thread": t.messages(thread), "links": t.links(ctx, links)}, nil
}

func (t *toolbox) getApplication(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[struct {
		App string `json:"app"`
	}](in)
	if err != nil {
		return nil, err
	}
	if err := t.requireApp(ctx, args.App); err != nil {
		return nil, err
	}
	var res struct {
		Application App       `json:"application"`
		Teams       []AppTeam `json:"responsible_teams"`
		Repos       []Repo    `json:"repositories"`
		Channels    []Channel `json:"channels"`
	}
	if err := t.api(ctx, "GET", "/apps/"+url.PathEscape(args.App), nil, &res); err != nil {
		return nil, err
	}
	a := res.Application
	teams := []string{}
	for _, tm := range res.Teams {
		teams = append(teams, tm.Name+" ("+tm.Role+")")
	}
	repos := []map[string]string{}
	for _, r := range res.Repos {
		repos = append(repos, map[string]string{"name": r.Name, "description": r.Description, "default_branch": r.DefaultBranch})
	}
	channels := []string{}
	for _, c := range res.Channels {
		channels = append(channels, "#"+c.Name)
	}
	return map[string]any{"key": a.Key, "name": a.Name, "description": a.Description, "owner_team": a.OwnerTeam.Name,
		"responsible_teams": teams, "repositories": repos, "channels": channels}, nil
}

func (t *toolbox) listFiles(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[struct {
		App  string `json:"app"`
		Repo string `json:"repo"`
		Ref  string `json:"ref"`
		Path string `json:"path"`
	}](in)
	if err != nil {
		return nil, err
	}
	if err := t.requireApp(ctx, args.App); err != nil {
		return nil, err
	}
	var out map[string]any
	q := url.Values{}
	if args.Ref != "" {
		q.Set("ref", args.Ref)
	}
	if args.Path != "" {
		q.Set("path", args.Path)
	}
	err = t.api(ctx, "GET", "/apps/"+url.PathEscape(args.App)+"/repos/"+url.PathEscape(args.Repo)+"/tree?"+q.Encode(), nil, &out)
	return out, err
}

const maxReadLines = 400

func (t *toolbox) readFile(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[struct {
		App   string `json:"app"`
		Repo  string `json:"repo"`
		Path  string `json:"path"`
		Ref   string `json:"ref"`
		Start int    `json:"start_line"`
		End   int    `json:"end_line"`
	}](in)
	if err != nil {
		return nil, err
	}
	if err := t.requireApp(ctx, args.App); err != nil {
		return nil, err
	}
	q := url.Values{"path": {args.Path}}
	if args.Ref != "" {
		q.Set("ref", args.Ref)
	}
	var blob struct {
		Commit  string `json:"commit"`
		Path    string `json:"path"`
		Content string `json:"content"`
		Binary  bool   `json:"binary"`
	}
	if err := t.api(ctx, "GET", "/apps/"+url.PathEscape(args.App)+"/repos/"+url.PathEscape(args.Repo)+"/blob?"+q.Encode(), nil, &blob); err != nil {
		return nil, err
	}
	if blob.Binary {
		return nil, errors.New("binary file")
	}
	lines := strings.Split(strings.TrimSuffix(blob.Content, "\n"), "\n")
	start := max(args.Start, 1)
	end := args.End
	if end < start || end > len(lines) {
		end = len(lines)
	}
	if end-start+1 > maxReadLines {
		end = start + maxReadLines - 1
	}
	var b strings.Builder
	for i := start; i <= end && i <= len(lines); i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}
	return map[string]any{"commit": blob.Commit, "path": blob.Path, "start_line": start, "end_line": end,
		"total_lines": len(lines), "content": b.String()}, nil
}

func (t *toolbox) listMyTodos(ctx context.Context, in json.RawMessage) (any, error) {
	var todos []Todo
	if err := t.api(ctx, "GET", "/todos", nil, &todos); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, td := range todos {
		item := map[string]any{"title": td.Title, "done": td.Done, "created_at": td.CreatedAt.UTC().Format("2006-01-02")}
		if td.Source != nil {
			item["from"] = td.Source.Title + " (" + td.Source.Subtitle + ")"
		}
		out = append(out, item)
	}
	return map[string]any{"todos": out}, nil
}

func (t *toolbox) proposeIssue(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[ProposalInput](in)
	if err != nil {
		return nil, err
	}
	if args.App == "" && t.channel.AppKey != nil {
		args.App = *t.channel.AppKey
	}
	args.Title = strings.TrimSpace(args.Title)
	if args.Title == "" {
		return nil, errors.New("title is required")
	}
	if err := t.requireApp(ctx, args.App); err != nil {
		return nil, err
	}
	if err := t.api(ctx, "GET", "/apps/"+url.PathEscape(args.App), nil, nil); err != nil {
		return nil, err
	}
	return t.propose(ctx, "issue", ProposalInput{App: args.App, Title: args.Title, Body: args.Body})
}

func (t *toolbox) proposeTodo(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[ProposalInput](in)
	if err != nil {
		return nil, err
	}
	args.Title = strings.TrimSpace(args.Title)
	if args.Title == "" {
		return nil, errors.New("title is required")
	}
	return t.propose(ctx, "todo", ProposalInput{Title: args.Title})
}

func (t *toolbox) proposeIssueComment(ctx context.Context, in json.RawMessage) (any, error) {
	args, err := decode[ProposalInput](in)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Body) == "" {
		return nil, errors.New("body is required")
	}
	if err := t.requireApp(ctx, args.App); err != nil {
		return nil, err
	}
	if err := t.api(ctx, "GET", fmt.Sprintf("/apps/%s/issues/%d", url.PathEscape(args.App), args.Number), nil, nil); err != nil {
		return nil, err
	}
	return t.propose(ctx, "issue_comment", ProposalInput{App: args.App, Number: args.Number, Body: args.Body})
}

func (t *toolbox) propose(ctx context.Context, kind string, in ProposalInput) (any, error) {
	p, err := t.s.createProposal(ctx, t.runID, kind, in)
	if err != nil {
		return nil, err
	}
	if err := t.s.refreshReply(ctx, t.runID, "message.updated"); err != nil {
		return nil, err
	}
	out, err := proposalJSON(p)
	return json.RawMessage(out), err
}
