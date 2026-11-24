// Package openai implements the OpenAI-shaped /v1 media endpoints.
package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/store"
)

type handler struct {
	st     *store.Store
	pool   *accounts.Pool
	now    func() time.Time
	stream *http.Client
}

// New returns the /v1 handler (mounted behind gateway auth by server).
func New(st *store.Store, pool *accounts.Pool) http.Handler {
	h := &handler{st: st, pool: pool, now: time.Now, stream: &http.Client{}}
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/models", h.listModels)
	m.HandleFunc("POST /v1/images/generations", h.generateImage)
	m.HandleFunc("POST /v1/images/edits", h.editImage)
	m.HandleFunc("POST /v1/videos", h.createVideo)
	m.HandleFunc("GET /v1/videos", h.listVideos)
	m.HandleFunc("GET /v1/videos/{id}", h.getVideo)
	m.HandleFunc("GET /v1/videos/{id}/content", h.videoContent)
	m.HandleFunc("POST /v1/audio/speech", h.speech)
	return m
}

func bad(format string, args ...any) error { return httpx.BadRequest(format, args...) }

func adapter(res accounts.Resolved, kind string) (provider.Adapter, error) {
	a, ok := provider.Get(res.Provider.Type)
	if !ok {
		return nil, fmt.Errorf("%w: provider type %s is not registered", media.ErrBadRequest, res.Provider.Type)
	}
	switch kind {
	case "image":
		if _, ok := a.(provider.ImageGenerator); ok {
			return a, nil
		}
	case "video":
		if _, ok := a.(provider.VideoGenerator); ok {
			return a, nil
		}
	case "speech":
		if _, ok := a.(provider.SpeechGenerator); ok {
			return a, nil
		}
	}
	return nil, fmt.Errorf("%w: provider type %s does not support %s", media.ErrWrongKind, res.Provider.Type, kind)
}

// logFill annotates the request-log entry (nil-safe).
func logFill(r *http.Request, modelID string, res *accounts.Resolved, result *accounts.Result) {
	l := httpx.Log(r.Context())
	if l == nil {
		return
	}
	l.ModelID = modelID
	if res != nil {
		l.ProviderID = res.Provider.ID
	}
	if result != nil {
		l.AccountID = result.AccountID
		l.Attempts = result.Attempts
	}
}

// ---- input parsing helpers ----

// parseInputString turns a JSON string input into media.Input: data: URLs are
// decoded, http(s):// stays a URL (never fetched — no SSRF).
func parseInputString(s string) (media.Input, error) {
	if strings.HasPrefix(s, "data:") {
		return media.ParseDataURL(s)
	}
	return media.Input{URL: s}, nil
}

// parseInputValue accepts a string or an object like {"image_url": ...} / {"url": ...}.
func parseInputValue(v any) (media.Input, error) {
	switch t := v.(type) {
	case string:
		return parseInputString(t)
	case map[string]any:
		if u, ok := t["image_url"]; ok {
			if s, ok := u.(string); ok {
				return parseInputString(s)
			}
			if m, ok := u.(map[string]any); ok {
				if s, _ := m["url"].(string); s != "" {
					return parseInputString(s)
				}
			}
		}
		if s, _ := t["url"].(string); s != "" {
			return parseInputString(s)
		}
	}
	return media.Input{}, bad("unsupported input object")
}

func inputValues(v any) ([]media.Input, error) {
	var out []media.Input
	switch t := v.(type) {
	case nil:
	case string:
		in, err := parseInputString(t)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	case map[string]any:
		in, err := parseInputValue(t)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	case []any:
		for _, item := range t {
			in, err := parseInputValue(item)
			if err != nil {
				return nil, err
			}
			out = append(out, in)
		}
	default:
		return nil, bad("unsupported input value")
	}
	return out, nil
}

func fileInput(fh *multipart.FileHeader) (media.Input, error) {
	f, err := fh.Open()
	if err != nil {
		return media.Input{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return media.Input{}, err
	}
	return media.Input{Name: fh.Filename, MIME: fh.Header.Get("Content-Type"), Data: data}, nil
}

// collectFiles gathers uploaded files under the first matching field names.
func collectFiles(r *http.Request, names ...string) ([]media.Input, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	var out []media.Input
	for _, name := range names {
		for _, fh := range r.MultipartForm.File[name] {
			in, err := fileInput(fh)
			if err != nil {
				return nil, err
			}
			out = append(out, in)
		}
	}
	return out, nil
}

func firstFile(r *http.Request, names ...string) (*media.Input, error) {
	files, err := collectFiles(r, names...)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	return &files[0], nil
}

func parseN(v any, def int) (int, error) {
	if v == nil {
		return def, nil
	}
	var n int64
	switch t := v.(type) {
	case float64:
		n = int64(t)
	case string:
		var err error
		n, err = strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0, bad("invalid n/count: %q", t)
		}
	case json.Number:
		var err error
		n, err = t.Int64()
		if err != nil {
			return 0, bad("invalid n: %v", t)
		}
	default:
		return 0, bad("invalid n type")
	}
	if n < 1 || n > 10 {
		return 0, bad("n must be between 1 and 10")
	}
	return int(n), nil
}

// strOf normalizes a JSON scalar (string or number) to a string.
func strOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	}
	return ""
}

func parseBoolPtr(s string) (*bool, error) {
	if s == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return nil, bad("invalid boolean value %q", s)
	}
	return &b, nil
}
