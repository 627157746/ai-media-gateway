package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-media-gateway/internal/media"
)

type ProviderConfig struct {
	ID       string
	Type     string
	BaseURL  string
	ProxyURL string // "" inherits global/env, "direct" bypasses, else a proxy URL
	Options  json.RawMessage
}

type AccountConfig struct {
	ID      string
	Name    string
	APIKey  string
	BaseURL string
	Options json.RawMessage
}

type ModelConfig struct {
	ID       string
	Kind     media.Kind
	Upstream string
	Options  json.RawMessage
}

// Call is one upstream attempt: resolved provider + account + model.
// HTTP is for vendor API calls (has the upstream timeout); Stream is for
// downloads/streaming and must have no Timeout. Timeout is the per-call
// adapter budget (e.g. image polling).
type Call struct {
	Provider ProviderConfig
	Account  AccountConfig
	Model    ModelConfig
	HTTP     *http.Client
	Stream   *http.Client
	Timeout  time.Duration
}

// BaseURL picks account override > provider > def.
func (c Call) BaseURL(def string) string {
	u := c.Account.BaseURL
	if u == "" {
		u = c.Provider.BaseURL
	}
	if u == "" {
		u = def
	}
	return strings.TrimSuffix(u, "/")
}

type Adapter interface {
	Type() string
}

type ImageGenerator interface {
	GenerateImage(ctx context.Context, c Call, r media.ImageRequest) (*media.ImageResult, error)
}

type VideoGenerator interface {
	SubmitVideo(ctx context.Context, c Call, r media.VideoRequest) (media.TaskHandle, error)
	PollVideo(ctx context.Context, c Call, h media.TaskHandle) (media.TaskState, error)
}

type SpeechGenerator interface {
	Speak(ctx context.Context, c Call, r media.SpeechRequest) (*media.Audio, error)
}

// ModelQuery filters an upstream model catalog listing: free-text Q, a
// vendor Category, a pagination Cursor, or explicit endpoint IDs
// (find mode).
type ModelQuery struct {
	Q        string
	Category string
	Cursor   string
	IDs      []string
}

// UpstreamModel is one importable entry of a provider's model catalog.
type UpstreamModel struct {
	ID          string     `json:"id"` // upstream id / endpoint_id
	Name        string     `json:"name"`
	Kind        media.Kind `json:"kind"` // image|video|speech; non-importable entries are dropped
	Category    string     `json:"category"`
	Description string     `json:"description,omitempty"`
	Deprecated  bool       `json:"deprecated,omitempty"`
}

type ModelPage struct {
	Models     []UpstreamModel
	NextCursor string
}

// ModelLister is implemented by adapters that can list upstream models.
type ModelLister interface {
	ListModels(ctx context.Context, c Call, q ModelQuery) (ModelPage, error)
}

var (
	regMu sync.RWMutex
	reg   = map[string]Adapter{}
)

// Register adds an adapter under its type name (e.g. "fal").
func Register(typ string, a Adapter) {
	regMu.Lock()
	reg[typ] = a
	regMu.Unlock()
}

func Get(typ string) (Adapter, bool) {
	regMu.RLock()
	a, ok := reg[typ]
	regMu.RUnlock()
	return a, ok
}

func Types() []string {
	regMu.RLock()
	out := make([]string, 0, len(reg))
	for t := range reg {
		out = append(out, t)
	}
	regMu.RUnlock()
	sort.Strings(out)
	return out
}
