// Package httpx holds shared HTTP helpers: OpenAI-shaped errors, JSON
// responses, and the per-request log entry handlers can annotate.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/store"
)

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error emits the OpenAI error shape {"error":{message,type,code}}.
func Error(w http.ResponseWriter, status int, msg, typ, code string) {
	WriteJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    typ,
			"code":    code,
		},
	})
}

// WriteError maps an internal error to an OpenAI-shaped HTTP error response.
func WriteError(w http.ResponseWriter, err error) {
	var ue *media.UpstreamError
	var nae *accounts.NoAccountError
	switch {
	case errors.Is(err, media.ErrBadRequest):
		Error(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "bad_request")
	case errors.Is(err, media.ErrModelNotFound):
		Error(w, http.StatusNotFound, err.Error(), "invalid_request_error", "model_not_found")
	case errors.Is(err, media.ErrWrongKind):
		Error(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "wrong_kind")
	case errors.As(err, &nae):
		Error(w, http.StatusServiceUnavailable, nae.Error(), "server_error", "no_account_available")
	case errors.Is(err, store.ErrNotFound):
		Error(w, http.StatusNotFound, err.Error(), "not_found", "not_found")
	case errors.Is(err, store.ErrConflict):
		Error(w, http.StatusConflict, err.Error(), "conflict", "conflict")
	case errors.As(err, &ue):
		writeUpstreamError(w, ue)
	default:
		Error(w, http.StatusInternalServerError, err.Error(), "internal_error", "internal_error")
	}
}

// Fail writes err as an error response and records it on the request log entry.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	if l := Log(r.Context()); l != nil && l.Error == "" {
		l.Error = err.Error()
	}
	WriteError(w, err)
}

func writeUpstreamError(w http.ResponseWriter, ue *media.UpstreamError) {
	msg := ue.Message
	if ue.Code != "" {
		msg = ue.Code + ": " + msg
	}
	switch ue.Class {
	case media.ClassAuth:
		Error(w, http.StatusBadGateway, msg, "upstream_auth", ue.Code)
	case media.ClassQuota:
		Error(w, http.StatusPaymentRequired, msg, "insufficient_quota", ue.Code)
	case media.ClassRateLimit:
		if ue.RetryAfter > 0 {
			secs := int((ue.RetryAfter + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		Error(w, http.StatusTooManyRequests, msg, "rate_limit_exceeded", ue.Code)
	case media.ClassInvalid:
		Error(w, http.StatusBadRequest, msg, "invalid_request_error", ue.Code)
	case media.ClassContent:
		Error(w, http.StatusBadRequest, msg, "content_policy_violation", ue.Code)
	default:
		Error(w, http.StatusBadGateway, msg, "upstream_error", ue.Code)
	}
}

// BadRequest builds a 400-mappable validation error.
func BadRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", media.ErrBadRequest, fmt.Sprintf(format, args...))
}

// ---- request log entry in context ----

type logKey struct{}

// WithLog stores a request-log entry on the context.
func WithLog(ctx context.Context, e *store.RequestLog) context.Context {
	return context.WithValue(ctx, logKey{}, e)
}

// Log returns the request-log entry, or nil.
func Log(ctx context.Context) *store.RequestLog {
	e, _ := ctx.Value(logKey{}).(*store.RequestLog)
	return e
}
