package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type Todo struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Done      bool       `json:"done"`
	CreatedAt time.Time  `json:"created_at"`
	DoneAt    *time.Time `json:"done_at"`
	// Source is what the todo was created from; opening it jumps there.
	Source *Entity `json:"source"`
}

func (s *Server) loadTodos(r *http.Request, where string, args ...any) ([]Todo, error) {
	ctx := r.Context()
	rows, err := s.DB.Query(ctx, `
		select t.id, t.title, t.done, t.created_at, t.done_at, l.to_type, l.to_id
		  from todos t
		  left join lateral (
			select to_type, to_id from links
			 where from_type = 'todo' and from_id = t.id::text and kind = 'created_from'
			 order by created_at limit 1
		  ) l on true
		 where `+where+`
		 order by t.done, t.created_at desc`, args...)
	if err != nil {
		return nil, err
	}
	type raw struct {
		Todo
		srcType, srcID *string
	}
	raws, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (raw, error) {
		var x raw
		err := row.Scan(&x.ID, &x.Title, &x.Done, &x.CreatedAt, &x.DoneAt, &x.srcType, &x.srcID)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	todos := make([]Todo, 0, len(raws))
	for _, x := range raws {
		if x.srcType != nil {
			e, err := s.resolve(ctx, Ref{Type: *x.srcType, ID: *x.srcID})
			if err != nil {
				return nil, err
			}
			x.Source = &e
		}
		todos = append(todos, x.Todo)
	}
	return todos, nil
}

func (s *Server) listTodos(w http.ResponseWriter, r *http.Request) error {
	todos, err := s.loadTodos(r, `t.user_id = $1`, currentUser(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, todos)
	return nil
}

func (s *Server) writeTodo(w http.ResponseWriter, r *http.Request, id string, status int) error {
	u := currentUser(r)
	todos, err := s.loadTodos(r, `t.id = $1 and t.user_id = $2`, id, u.ID)
	if err != nil {
		return err
	}
	if len(todos) == 0 {
		return errNotFound
	}
	s.Hub.Publish("user:"+u.ID, "todo.updated", todos[0])
	writeJSON(w, status, todos[0])
	return nil
}

// createTodo adds a personal todo, optionally pointing at its source (a
// message, issue or code range). Without a title it takes the source's.
func (s *Server) createTodo(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Title  string `json:"title"`
		Source *Ref   `json:"source"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	in.Title = strings.TrimSpace(in.Title)
	if in.Source != nil {
		e, err := s.resolve(ctx, *in.Source)
		if err != nil {
			return err
		}
		if e.Missing || !e.visibleFor(currentUser(r).ID) {
			return badRequest(in.Source.Type + " does not exist")
		}
		if in.Source.Type == "message" {
			if _, err := s.visibleMessage(r, in.Source.ID); err != nil {
				return err
			}
		}
		if in.Title == "" {
			in.Title = e.Title
		}
	}
	if in.Title == "" {
		return badRequest("title is required")
	}
	u := currentUser(r)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `insert into todos (user_id, title) values ($1, $2) returning id`, u.ID, in.Title).Scan(&id); err != nil {
		return err
	}
	if in.Source != nil {
		if err := addLink(ctx, tx, Ref{Type: "todo", ID: id}, *in.Source, LinkCreatedFrom, nil, u.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.writeTodo(w, r, id, http.StatusCreated)
}

func (s *Server) updateTodo(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "todoID")
	var in struct {
		Title *string `json:"title"`
		Done  *bool   `json:"done"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u := currentUser(r)
	ctx := r.Context()
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return badRequest("title is required")
		}
		if _, err := s.DB.Exec(ctx, `update todos set title = $3 where id = $1 and user_id = $2`, id, u.ID, t); err != nil {
			return err
		}
	}
	if in.Done != nil {
		if _, err := s.DB.Exec(ctx, `update todos set done = $3, done_at = case when $3 then now() end where id = $1 and user_id = $2`,
			id, u.ID, *in.Done); err != nil {
			return err
		}
	}
	return s.writeTodo(w, r, id, http.StatusOK)
}

func (s *Server) deleteTodo(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "todoID")
	u := currentUser(r)
	tag, err := s.DB.Exec(r.Context(), `delete from todos where id = $1 and user_id = $2`, id, u.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	if _, err := s.DB.Exec(r.Context(), `delete from links where (from_type = 'todo' and from_id = $1) or (to_type = 'todo' and to_id = $1)`, id); err != nil {
		return err
	}
	s.Hub.Publish("user:"+u.ID, "todo.deleted", map[string]string{"id": id})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
