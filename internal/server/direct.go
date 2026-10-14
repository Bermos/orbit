package server

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Bermos/orbit/internal/agent"
)

// Private chats with agents. Each is a channel of kind "direct" that belongs
// to one person and one agent; a person can keep as many as they like, one
// per topic. The agent answers every message, in the chat itself rather than
// in a thread, and acts with the lower of the person's permissions and its
// own, with access to their personal data as for "answer only me".

const maxChatTitle = 60

// chatTitle derives a chat's title from its first message.
func chatTitle(body string) string {
	title := strings.Join(strings.Fields(body), " ")
	return truncate(title, maxChatTitle)
}

// byUsername returns the agent whose member has the given username.
func (a *agentService) byUsername(name string) (agent.Definition, bool) {
	if a == nil {
		return agent.Definition{}, false
	}
	def, ok := a.defs[name]
	return def, ok
}

// replyRoot is the thread an agent answers trigger in: its thread, or a new
// thread under it. In a private chat a top-level message is answered in the
// chat itself.
func replyRoot(ch Channel, trigger Message) *string {
	if trigger.ThreadID != nil {
		return trigger.ThreadID
	}
	if ch.direct() {
		return nil
	}
	return &trigger.ID
}

func (s *Server) listDirectChats(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.DB.Query(r.Context(), `select `+channelCols+` from channels c
		where c.kind = 'direct' and c.owner_id = $1 order by coalesce(c.last_message_at, c.created_at) desc limit 200`, currentUser(r).ID)
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

func (s *Server) createDirectChat(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Agent string `json:"agent"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(in.Agent), "@")))
	def, ok := s.agents.byUsername(name)
	if !ok {
		return badRequest("no agent named " + in.Agent)
	}
	rows, err := s.DB.Query(r.Context(), `insert into channels as c (kind, name, owner_id, agent_id) values ('direct', '', $1, $2)
		returning `+channelCols, currentUser(r).ID, s.agents.userIDs[def.Name])
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

func (s *Server) deleteDirectChat(w http.ResponseWriter, r *http.Request) error {
	ch, err := s.loadChannel(r.Context(), r, chi.URLParam(r, "channelID"))
	if err != nil {
		return err
	}
	if !ch.direct() {
		return errNotFound
	}
	if _, err := s.DB.Exec(r.Context(), `delete from channels where id = $1`, ch.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
