package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/go-chi/chi/v5"
)

// apiCallError is a non-2xx answer from an in-process API call.
type apiCallError struct {
	Status int
	Msg    string
}

func (e *apiCallError) Error() string { return e.Msg }

// callAPI runs a request against the public API in-process, as user u. Agent
// tools and confirmed proposals use it so they pass exactly the permission
// checks and side effects (links, notes, events) a person's request would.
// out, if not nil, receives the decoded response.
func (s *Server) callAPI(ctx context.Context, u User, method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	// Drop the caller's routing state (when called from a handler) so the
	// internal router routes the request afresh.
	ctx = context.WithValue(context.WithValue(ctx, chi.RouteCtxKey, nil), ctxKey{}, u)
	req := httptest.NewRequestWithContext(ctx, method, "/api/v1"+path, &body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.internal.ServeHTTP(rec, req)
	if rec.Code >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(rec.Body.Bytes(), &e)
		if e.Error == "" {
			e.Error = http.StatusText(rec.Code)
		}
		return &apiCallError{Status: rec.Code, Msg: fmt.Sprintf("%s (HTTP %d)", e.Error, rec.Code)}
	}
	if out != nil && rec.Code != http.StatusNoContent {
		return json.Unmarshal(rec.Body.Bytes(), out)
	}
	return nil
}
