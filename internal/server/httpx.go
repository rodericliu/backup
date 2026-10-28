package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	json "encoding/json/v2"

	"github.com/rodericliu/backup/internal/store"
)

// maxBody bounds JSON request bodies.
const maxBody = 1 << 20

// Error is the JSON error body of every API.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorBody struct {
	Error Error `json:"error"`
}

// apiError is an error with an HTTP status and a stable code.
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.code + ": " + e.msg }

func newError(status int, code, format string, args ...any) *apiError {
	return &apiError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

// Common error codes.
var (
	errUnauthorized = newError(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	errForbidden    = newError(http.StatusForbidden, "FORBIDDEN", "forbidden")
)

func badRequest(format string, args ...any) *apiError {
	return newError(http.StatusBadRequest, "BAD_REQUEST", format, args...)
}

func notFound(what string) *apiError {
	return newError(http.StatusNotFound, "NOT_FOUND", "%s not found", what)
}

func conflict(format string, args ...any) *apiError {
	return newError(http.StatusConflict, "CONFLICT", format, args...)
}

// handlerFunc is an HTTP handler that returns an error instead of writing it.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// handle adapts a handlerFunc, writing returned errors as JSON. Errors that
// are not *apiError are logged and reported as internal errors.
func (s *Server) handle(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			s.writeError(w, r, err)
		}
	})
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	ae, ok := errors.AsType[*apiError](err)
	if !ok {
		switch {
		case errors.Is(err, store.ErrNotFound):
			ae = notFound("resource")
		case errors.Is(err, store.ErrConflict):
			ae = conflict("already exists")
		default:
			s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
			ae = newError(http.StatusInternalServerError, "INTERNAL", "internal server error")
		}
	}
	writeJSON(w, ae.status, errorBody{Error{Code: ae.code, Message: ae.msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return json.MarshalWrite(w, v)
}

// readJSON decodes a JSON request body into v, rejecting unknown fields.
func readJSON(r *http.Request, v any) error {
	body := http.MaxBytesReader(nil, r.Body, maxBody)
	b, err := io.ReadAll(body)
	if err != nil {
		return badRequest("read body: %v", err)
	}
	if err := json.Unmarshal(b, v, json.RejectUnknownMembers(true)); err != nil {
		return badRequest("invalid JSON: %v", err)
	}
	return nil
}

// pathID parses a numeric path parameter.
func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("invalid %s %q", name, r.PathValue(name))
	}
	return id, nil
}

// statusWriter records the response status for access logs.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// accessLog logs requests at debug level and server errors at warn level,
// and recovers from panics.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", v)
				if sw.status == 0 {
					writeJSON(sw, http.StatusInternalServerError, errorBody{Error{Code: "INTERNAL", Message: "internal server error"}})
				}
			}
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			lvl := slog.LevelDebug
			if status >= 500 {
				lvl = slog.LevelWarn
			}
			s.log.Log(r.Context(), lvl, "http", "method", r.Method, "path", r.URL.Path, "status", status,
				"duration", time.Since(start), "remote", r.RemoteAddr)
		}()
		next.ServeHTTP(sw, r)
	})
}
