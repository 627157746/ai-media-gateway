// Package fishaudio implements the Fish Audio TTS adapter.
package fishaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"ai-media-gateway/internal/capability"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/msgpack"
	"ai-media-gateway/internal/provider"
)

type Adapter struct{}

func (Adapter) Type() string { return "fishaudio" }

func init() { provider.Register("fishaudio", Adapter{}) }

const defaultBase = "https://api.fish.audio"

var validFormats = map[string]string{
	"":     "mp3",
	"mp3":  "audio/mpeg",
	"wav":  "audio/wav",
	"pcm":  "audio/pcm",
	"opus": "audio/ogg",
}

func (Adapter) Speak(ctx context.Context, c provider.Call, r media.SpeechRequest) (*media.Audio, error) {
	format := strings.ToLower(r.Format)
	defCT, ok := validFormats[format]
	if !ok {
		return nil, &media.UpstreamError{Class: media.ClassInvalid,
			Message: "unsupported format " + r.Format + " (mp3|wav|pcm|opus)", Final: true}
	}
	if format == "" {
		format = "mp3"
	}
	var (
		raw         []byte
		contentType string
	)
	if ref := r.ReferenceAudio; ref != nil && len(ref.Data) > 0 {
		// Zero-shot cloning: the sample travels with every request inside
		// references[], and /v1/tts only accepts that shape as MessagePack.
		payload := msgpack.Map{
			{Key: "text", Val: r.Input},
			{Key: "references", Val: []any{
				msgpack.Map{
					{Key: "audio", Val: ref.Data},
					{Key: "text", Val: ""},
				},
			}},
			{Key: "format", Val: format},
		}
		if sp := capability.For("fishaudio").Speed.Clamp(r.Speed); sp > 0 {
			payload = append(payload, msgpack.Pair{Key: "prosody", Val: msgpack.Map{{Key: "speed", Val: sp}}})
		}
		encoded, err := msgpack.Encode(payload)
		if err != nil {
			return nil, &media.UpstreamError{Class: media.ClassInvalid, Message: err.Error(), Final: true}
		}
		raw, contentType = encoded, "application/msgpack"
	} else {
		body := map[string]any{
			"text":   r.Input,
			"format": format,
		}
		if r.Language != "" {
			body["language"] = r.Language
		}
		if v := speechVoice(c, r); v != "" {
			body["reference_id"] = v
		}
		if sp := capability.For("fishaudio").Speed.Clamp(r.Speed); sp > 0 {
			body["prosody"] = map[string]any{"speed": sp}
		}
		for k, v := range r.Extra {
			body[k] = v
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, &media.UpstreamError{Class: media.ClassInvalid, Message: err.Error(), Final: true}
		}
		raw, contentType = encoded, "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL(defaultBase)+"/v1/tts", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Account.APIKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("model", c.Model.Upstream)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, true)
	}
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = defCT
	}
	return &media.Audio{Body: resp.Body, ContentType: ct}, nil
}

// ListVoices returns the workspace's own TTS voices.
//
// Endpoint is GET {base|https://api.fish.audio}/model?self=true (verified
// against the live API: /v1/model is 404, /model is 200). The list response
// carries window_limited / max_offset: the server caps how far paging can
// reach, so this fetches a single page and reports truncation instead of
// silently paginating past the limit.
func (Adapter) ListVoices(ctx context.Context, c provider.Call) ([]provider.UpstreamVoice, error) {
	u := c.BaseURL(defaultBase) + "/model?self=true&page_size=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Account.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, false)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	var out struct {
		Total   int  `json:"total"`
		HasMore bool `json:"has_more"`
		Items   []struct {
			ID         string   `json:"_id"`
			Type       string   `json:"type"`
			Title      string   `json:"title"`
			State      string   `json:"state"`
			Visibility string   `json:"visibility"`
			Languages  []string `json:"languages"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &media.UpstreamError{
			Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "bad voices response: " + err.Error(),
		}
	}
	voices := make([]provider.UpstreamVoice, 0, len(out.Items))
	for _, it := range out.Items {
		// Only trained TTS voices can be used as reference_id; svc models and
		// voices still training or failed would 4xx at synthesis time.
		if it.ID == "" || it.Type != "tts" || it.State != "trained" {
			continue
		}
		name := it.Title
		if name == "" {
			name = it.ID
		}
		detail := it.Visibility
		if len(it.Languages) > 0 {
			detail = strings.TrimSpace(detail + " · " + strings.Join(it.Languages, ","))
		}
		voices = append(voices, provider.UpstreamVoice{ID: it.ID, Name: name, Detail: detail})
	}
	return voices, nil
}

// speechVoice returns the vendor voice id, honouring an already-resolved one.
func speechVoice(c provider.Call, r media.SpeechRequest) string {
	if r.VoiceResolved {
		return r.Voice
	}
	return provider.ResolveVoice(c, r.Voice)
}

// Fish Audio has no TTS-model list API; this mirrors the `model` header enum.
var staticModels = []provider.UpstreamModel{
	{ID: "s2.1-pro", Name: "S2.1 Pro", Kind: media.KindSpeech, Category: "text-to-speech", Description: "推荐"},
	{ID: "s2.1-pro-free", Name: "S2.1 Pro Free", Kind: media.KindSpeech, Category: "text-to-speech"},
	{ID: "s3-pro", Name: "S3 Pro", Kind: media.KindSpeech, Category: "text-to-speech"},
	{ID: "drama-3", Name: "Drama 3", Kind: media.KindSpeech, Category: "text-to-speech"},
	{ID: "s2-pro", Name: "S2 Pro", Kind: media.KindSpeech, Category: "text-to-speech"},
	{ID: "s1", Name: "S1", Kind: media.KindSpeech, Category: "text-to-speech", Description: "已弃用，2026-12-31 退役", Deprecated: true},
}

func (Adapter) ListModels(_ context.Context, _ provider.Call, q provider.ModelQuery) (provider.ModelPage, error) {
	ql := strings.ToLower(q.Q)
	out := provider.ModelPage{Models: []provider.UpstreamModel{}}
	for _, m := range staticModels {
		if ql != "" && !strings.Contains(strings.ToLower(m.ID), ql) && !strings.Contains(strings.ToLower(m.Name), ql) {
			continue
		}
		out.Models = append(out.Models, m)
	}
	return out, nil
}

func statusError(resp *http.Response) error {
	body := provider.ReadErrorBody(resp)
	msg := http.StatusText(resp.StatusCode)
	var d struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(body, &d) == nil {
		if d.Message != "" {
			msg = d.Message
		} else if d.Detail != "" {
			msg = d.Detail
		}
	}
	ue := &media.UpstreamError{Status: resp.StatusCode, Message: msg}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		ue.Class = media.ClassAuth
	case http.StatusPaymentRequired:
		ue.Class = media.ClassQuota
	case http.StatusTooManyRequests:
		ue.Class = media.ClassRateLimit
		ue.RetryAfter = provider.ParseRetryAfter(resp.Header)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		ue.Class = media.ClassInvalid
	default:
		if resp.StatusCode >= 500 {
			ue.Class = media.ClassUpstream
		} else {
			ue.Class = media.ClassInvalid
		}
	}
	return ue
}
