package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/store"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyAPIKey
)

// APIKeyFromCtx returns the authenticated gateway key, if any.
func APIKeyFromCtx(ctx context.Context) *store.APIKey {
	k, _ := ctx.Value(ctxKeyAPIKey).(*store.APIKey)
	return k
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = "req_" + hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, id)))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer for http.NewResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// loggedEndpoint reports whether a request gets a request_logs row: only
// model-generation calls — listings, polls and downloads are just noise.
func loggedEndpoint(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/images/generations", "/v1/images/edits", "/v1/videos", "/v1/audio/speech":
		return true
	}
	return false
}

// requestLog emits one slog line per request and records generation calls
// in request_logs. Authorization headers and bodies are never logged.
func requestLog(st *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var entry *store.RequestLog
		if loggedEndpoint(r) {
			entry = &store.RequestLog{Endpoint: r.Method + " " + r.URL.Path}
			r = r.WithContext(httpx.WithLog(r.Context(), entry))
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		lat := time.Since(start)
		reqID, _ := r.Context().Value(ctxKeyRequestID).(string)
		slog.Info("request",
			"request_id", reqID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"latency_ms", lat.Milliseconds(),
		)
		if entry != nil {
			entry.Status = sw.status
			entry.LatencyMs = lat.Milliseconds()
			_ = st.InsertRequestLog(context.Background(), entry)
		}
	})
}

func cors(next http.Handler, origins []string) http.Handler {
	star := slices.Contains(origins, "*")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allow := ""
		if star {
			allow = "*"
		} else if origin != "" && slices.Contains(origins, origin) {
			allow = origin
		}
		h := w.Header()
		if allow != "" {
			h.Set("Access-Control-Allow-Origin", allow)
			if !star {
				h.Add("Vary", "Origin")
			}
		}
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		reqHeaders := r.Header.Get("Access-Control-Request-Headers")
		if reqHeaders != "" {
			h.Set("Access-Control-Allow-Headers", reqHeaders)
		} else {
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id, Range")
		}
		h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Type, X-Request-Id, Content-Range, Accept-Ranges")
		h.Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bodyLimit(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if rest, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(rest)
	}
	return ""
}

// gatewayAuth accepts an sk-mg-* gateway key, or the admin token itself
// (used by the built-in 试用 page, which carries no gateway key).
func gatewayAuth(st *store.Store, adminToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			httpx.Error(w, http.StatusUnauthorized, "missing API key", "invalid_request_error", "invalid_api_key")
			return
		}
		if adminToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(adminToken)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		k, err := st.AuthenticateKey(r.Context(), tok)
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, "invalid API key", "invalid_request_error", "invalid_api_key")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyAPIKey, k)))
	})
}

func adminAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(token)) != 1 {
			httpx.Error(w, http.StatusUnauthorized, "invalid admin token", "invalid_request_error", "invalid_admin_token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				reqID, _ := r.Context().Value(ctxKeyRequestID).(string)
				slog.Error("panic", "request_id", reqID, "err", v, "path", r.URL.Path)
				httpx.Error(w, http.StatusInternalServerError, "internal error", "internal_error", "internal_error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
