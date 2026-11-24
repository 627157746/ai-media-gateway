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

// VideoFetcher is implemented by adapters whose result URL needs vendor
// authentication. The gateway's /content proxy fetches a finished video with a
// plain unauthenticated GET, which works for CDN links (fal) but fails for a
// vendor API that requires a key (Muse answers 401). Such an adapter streams the
// bytes itself, so the proxy never touches the vendor.
type VideoFetcher interface {
	// FetchVideo writes the finished video to w. resultURL is what PollVideo
	// reported, and rangeHeader carries the client's Range request through.
	FetchVideo(ctx context.Context, c Call, resultURL, rangeHeader string) (*http.Response, error)
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

// EndpointField is one supported input field of an upstream endpoint, as
// reported by the vendor's own schema. Declaring capabilities from this avoids
// hand-copying vendor docs into the gateway, which drifts as models change.
type EndpointField struct {
	// Name is the vendor's input field name, e.g. image_size or aspect_ratio.
	Name string `json:"name"`
	// Enum lists the accepted values when the vendor constrains the field.
	Enum []string `json:"enum,omitempty"`
	// Maximum is the numeric ceiling for integer fields such as num_images.
	Maximum *int `json:"maximum,omitempty"`
	// Minimum is the numeric floor, published for ranged integers such as a
	// video duration.
	Minimum *int `json:"minimum,omitempty"`
	// MaxItems caps array fields such as image_urls.
	MaxItems *int `json:"max_items,omitempty"`
	// Description is the vendor's own prose for the field. It carries the
	// semantics the field NAME does not: MiniMax calls the first frame
	// image_url while Alibaba calls it start_image_url, and only the prose
	// ("First frame of the generated video") says the two mean the same thing.
	Description string `json:"description,omitempty"`
	// Required marks a field the endpoint rejects the request without.
	Required bool `json:"required,omitempty"`
	// Default is the vendor default, useful for pre-filling a client control.
	Default any `json:"default,omitempty"`
}

// EndpointSchema is the declared input surface of one upstream endpoint.
type EndpointSchema struct {
	// Endpoint is the upstream id this schema describes.
	Endpoint string `json:"endpoint"`
	// Category is the vendor's own classification (text-to-image,
	// image-to-image, text-to-video, text-to-speech, ...).
	Category string `json:"category,omitempty"`
	// Fields are the endpoint's accepted inputs, keyed by vendor field name.
	Fields []EndpointField `json:"fields"`
}

// Field returns the named field.
func (s EndpointSchema) Field(name string) (EndpointField, bool) {
	for _, f := range s.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return EndpointField{}, false
}

// SchemaLister is implemented by adapters whose vendor publishes a per-endpoint
// input schema, so the gateway can derive a model's capabilities instead of
// declaring them by hand.
type SchemaLister interface {
	EndpointSchema(ctx context.Context, c Call, endpoint string) (EndpointSchema, error)
}

// UpstreamVoice is one voice in the vendor's own library, as shown by the
// 从上游导入 picker in the admin UI.
type UpstreamVoice struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"` // category / visibility / language hints
}

// CloneRequest asks a vendor to create a durable voice from audio samples.
type CloneRequest struct {
	Name                  string
	Description           string
	Files                 [][]byte
	RemoveBackgroundNoise bool
}

// CloneResult is what the vendor returned for a clone.
type CloneResult struct {
	// VoiceID is the durable vendor id (ElevenLabs voice_id).
	VoiceID string
	// RequiresVerification means the vendor will not synthesise with this voice
	// until the account owner completes an extra verification step.
	RequiresVerification bool
}

// VoiceCloner is implemented by adapters that can create a durable voice.
// Fish Audio deliberately does not implement it: its cloning is zero-shot
// (the sample rides along with each synthesis request), so there is no vendor
// voice to create.
type VoiceCloner interface {
	CloneVoice(ctx context.Context, c Call, r CloneRequest) (*CloneResult, error)
}

// VoiceLister is implemented by adapters whose vendor exposes a voice library.
// fal has no such concept: its `voice` is a model-specific field, so it
// deliberately does not implement this interface.
type VoiceLister interface {
	ListVoices(ctx context.Context, c Call) ([]UpstreamVoice, error)
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
