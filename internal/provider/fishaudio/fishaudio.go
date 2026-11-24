// Package fishaudio implements the Fish Audio TTS adapter.
package fishaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"ai-media-gateway/internal/media"
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
	body := map[string]any{
		"text":   r.Input,
		"format": format,
	}
	if v := provider.ResolveVoice(c, r.Voice); v != "" {
		body["reference_id"] = v
	}
	if r.Speed > 0 {
		body["prosody"] = map[string]any{"speed": r.Speed}
	}
	for k, v := range r.Extra {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, &media.UpstreamError{Class: media.ClassInvalid, Message: err.Error(), Final: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL(defaultBase)+"/v1/tts", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Account.APIKey)
	req.Header.Set("Content-Type", "application/json")
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
