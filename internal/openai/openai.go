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
	"unicode/utf8"

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
	dbPath string // reference samples for zero-shot cloning live next to it
}

// New returns the /v1 handler (mounted behind gateway auth by server).
// dbPath locates the reference-audio directory (sibling of the SQLite file);
// an empty value only disables zero-shot samples, not synthesis.
func New(st *store.Store, pool *accounts.Pool, dbPath string) http.Handler {
	h := &handler{st: st, pool: pool, now: time.Now, dbPath: dbPath}
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/models", h.listModels)
	m.HandleFunc("POST /v1/images/generations", h.generateImage)
	m.HandleFunc("POST /v1/images/edits", h.editImage)
	m.HandleFunc("POST /v1/videos", h.createVideo)
	m.HandleFunc("GET /v1/videos", h.listVideos)
	m.HandleFunc("GET /v1/videos/{id}", h.getVideo)
	m.HandleFunc("GET /v1/videos/{id}/content", h.videoContent)
	m.HandleFunc("POST /v1/audio/speech", h.speech)
	m.HandleFunc("GET /v1/audio/voices", h.listVoices)
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

// logParams stores a normalized summary of the request's media parameters
// on the log entry (nil-safe). Media inputs are summarized — never bytes.
func logParams(r *http.Request, p map[string]any) {
	l := httpx.Log(r.Context())
	if l == nil || len(p) == 0 {
		return
	}
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	l.Params = b
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func putStr(p map[string]any, k, v string) {
	if v != "" {
		p[k] = v
	}
}

func putNum(p map[string]any, k string, v int) {
	if v != 0 {
		p[k] = v
	}
}

// inputSummary describes an Input without any of its bytes.
func inputSummary(in media.Input) map[string]any {
	m := map[string]any{}
	if in.URL != "" && !strings.HasPrefix(in.URL, "data:") {
		m["url"] = clipRunes(in.URL, 200)
		return m
	}
	// bytes, or a data: URL left undecoded — treated like bytes either way.
	if in.Name != "" {
		m["name"] = in.Name
	}
	if in.MIME != "" {
		m["mime"] = in.MIME
	}
	n := len(in.Data)
	if n == 0 && in.URL != "" {
		n = len(in.URL)
	}
	if n > 0 {
		m["bytes"] = n
	}
	return m
}

func inputsSummary(ins []media.Input) []any {
	out := make([]any, 0, len(ins))
	for _, in := range ins {
		out = append(out, inputSummary(in))
	}
	return out
}

// scrubExtra deep-copies a client extra map, eliding strings longer than
// 500 runes (e.g. base64 blobs) with a placeholder.
func scrubExtra(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = scrubExtra(x)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, x := range t {
			s[i] = scrubExtra(x)
		}
		return s
	case string:
		if utf8.RuneCountInString(t) > 500 {
			return fmt.Sprintf("<已省略 %d 字符>", utf8.RuneCountInString(t))
		}
		return t
	default:
		return v
	}
}

func imageParams(req media.ImageRequest) map[string]any {
	p := map[string]any{}
	putStr(p, "prompt", clipRunes(req.Prompt, 2000))
	putNum(p, "n", req.N)
	putStr(p, "size", req.Size)
	putStr(p, "quality", req.Quality)
	putStr(p, "background", req.Background)
	putStr(p, "resolution", req.Resolution)
	putStr(p, "response_format", req.ResponseFormat)
	if req.Edit {
		p["edit"] = true
	}
	if len(req.Images) > 0 {
		p["images"] = inputsSummary(req.Images)
	}
	if req.Mask != nil {
		p["mask"] = inputSummary(*req.Mask)
	}
	if len(req.Extra) > 0 {
		p["extra"] = scrubExtra(req.Extra)
	}
	return p
}

func videoParams(req media.VideoRequest) map[string]any {
	p := map[string]any{}
	putStr(p, "prompt", clipRunes(req.Prompt, 2000))
	putStr(p, "mode", req.Mode)
	putStr(p, "seconds", req.Seconds)
	putStr(p, "size", req.Size)
	putStr(p, "resolution", req.Resolution)
	putStr(p, "ratio", req.Ratio)
	if req.GenerateAudio != nil {
		p["generate_audio"] = *req.GenerateAudio
	}
	if req.Watermark != nil {
		p["watermark"] = *req.Watermark
	}
	if req.FirstFrame != nil {
		p["first_frame"] = inputSummary(*req.FirstFrame)
	}
	if req.LastFrame != nil {
		p["last_frame"] = inputSummary(*req.LastFrame)
	}
	if len(req.References) > 0 {
		p["references"] = inputsSummary(req.References)
	}
	if len(req.Videos) > 0 {
		p["videos"] = inputsSummary(req.Videos)
	}
	if len(req.Audios) > 0 {
		p["audios"] = inputsSummary(req.Audios)
	}
	if len(req.Extra) > 0 {
		p["extra"] = scrubExtra(req.Extra)
	}
	return p
}

func speechParams(req media.SpeechRequest) map[string]any {
	p := map[string]any{}
	putStr(p, "input", clipRunes(req.Input, 2000))
	putStr(p, "voice", req.Voice)
	putStr(p, "format", req.Format)
	putStr(p, "instructions", clipRunes(req.Instructions, 2000))
	if req.Speed > 0 {
		p["speed"] = req.Speed
	}
	if len(req.Extra) > 0 {
		p["extra"] = scrubExtra(req.Extra)
	}
	return p
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

// collectInputs is collectFiles plus plain form values: a field may carry an
// uploaded file or a data:/http(s) URL string (Infinite Canvas sends public
// references as URLs). URLs pass through to the vendor, never fetched.
func collectInputs(r *http.Request, names ...string) ([]media.Input, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	var out []media.Input
	for _, name := range names {
		files, err := collectFiles(r, name)
		if err != nil {
			return nil, err
		}
		out = append(out, files...)
		for _, v := range r.MultipartForm.Value[name] {
			if v = strings.TrimSpace(v); v == "" {
				continue
			}
			in, err := parseInputString(v)
			if err != nil {
				return nil, err
			}
			out = append(out, in)
		}
	}
	return out, nil
}

func firstInput(r *http.Request, names ...string) (*media.Input, error) {
	ins, err := collectInputs(r, names...)
	if err != nil || len(ins) == 0 {
		return nil, err
	}
	return &ins[0], nil
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
