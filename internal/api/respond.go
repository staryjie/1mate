package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"1mate/internal/service"
	"1mate/internal/store"
)

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

const (
	CodeBadRequest   = "bad_request"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeInternal     = "internal_error"
	CodeValidation   = "validation_failed"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Code: code, Message: message})
}

// failFrom maps a service error to a stable HTTP status and error code.
func (s *Server) failFrom(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, http.StatusNotFound, CodeNotFound, "resource not found")
	case errors.Is(err, service.ErrConflict):
		s.fail(w, http.StatusConflict, CodeConflict, cleanMessage(err))
	case errors.Is(err, service.ErrValidation):
		s.fail(w, http.StatusBadRequest, CodeValidation, cleanMessage(err))
	case errors.Is(err, service.ErrForbidden):
		s.fail(w, http.StatusForbidden, CodeForbidden, cleanMessage(err))
	case errors.Is(err, service.ErrUnreachable):
		s.fail(w, http.StatusBadGateway, "host_unreachable", cleanMessage(err))
	default:
		s.log.Error("request failed", "error", err)
		s.fail(w, http.StatusInternalServerError, CodeInternal, "internal error")
	}
}

func cleanMessage(err error) string {
	msg := err.Error()
	for _, prefix := range []string{"validation failed: ", "conflict: ", "forbidden: "} {
		if len(msg) > len(prefix) && msg[:len(prefix)] == prefix {
			return msg[len(prefix):]
		}
	}
	return msg
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		s.fail(w, http.StatusBadRequest, CodeBadRequest, "a JSON body is required")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		s.fail(w, http.StatusBadRequest, CodeBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) logger() *slog.Logger { return s.log }
