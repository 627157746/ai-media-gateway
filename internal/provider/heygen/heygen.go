// Package heygen implements the HeyGen text-to-speech adapter.
//
// HeyGen answers with a URL rather than audio bytes, so Speak makes a second
// request to fetch the file. That download is unbounded (it is a CDN object,
// not a generation call) and therefore uses Call.Stream, per the adapter rules.
package heygen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"ai-media-gateway/internal/capability"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type Adapter struct{}

func (Adapter) Type() string { return "heygen" }

func init() { provider.Register("heygen", Adapter{}) }

const defaultBase = "https://api.heygen.com"

// validFormats maps the OpenAI-style response_format to HeyGen's encoding.
// HeyGen only distinguishes mp3 from PCM-in-a-WAV-container.
var validFormats = map[string]string{
	"":     "mp3",
	"mp3":  "mp3",
	"wav":  "pcm",
	"pcm":  "pcm",
	"opus": "mp3", // not supported upstream; fall back to mp3
}

var contentTypes = map[string]string{
	"mp3": "audio/mpeg",
	"pcm": "audio/wav",
}

func invalid(format string, args ...any) *media.UpstreamError {
	return &media.UpstreamError{Class: media.ClassInvalid, Message: fmt.Sprintf(format, args...), Final: true}
}

func (Adapter) Speak(ctx context.Context, c provider.Call, r media.SpeechRequest) (*media.Audio, error) {
	format := strings.ToLower(r.Format)
	encoding, ok := validFormats[format]
	if !ok {
		return nil, invalid("unsupported response_format %q (mp3|wav|pcm)", r.Format)
	}

	voice := r.Voice
	if !r.VoiceResolved {
		voice = provider.ResolveVoice(c, voice)
	}
	// HeyGen requires voice_id; without this guard the vendor answers a generic
	// "invalid_parameter: Request validation failed", which reads like a schema
	// problem rather than a missing voice.
	if voice == "" {
		return nil, invalid("HeyGen 需要 voice_id：请在 voice 里填上游音色 ID，或在平台/模型的 options 里配置 default_voice")
	}

	body := map[string]any{
		"text":  r.Input,
		"model": c.Model.Upstream,
		"output_format": map[string]any{
			"encoding": encoding,
		},
	}
	if voice != "" {
		body["voice_id"] = voice
	}
	if r.Language != "" {
		body["language"] = r.Language
	}
	// Turbo models reject a speed field outright, so it is only sent otherwise.
	// HeyGen documents 0.75–1.25 and errors outside it, hence the clamp.
	if sp := capability.For("heygen").Speed.Clamp(r.Speed); sp > 0 && !strings.Contains(c.Model.Upstream, "turbo") {
		body["speed"] = sp
	}
	for k, v := range r.Extra {
		body[k] = v
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, invalid("cannot marshal request: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL(defaultBase)+"/v3/models/audio/tts", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.Account.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, true)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	var out struct {
		Data struct {
			AudioURL string  `json:"audio_url"`
			Duration float64 `json:"duration"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &media.UpstreamError{
			Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "bad tts response: " + err.Error(), Final: true,
		}
	}
	if out.Data.AudioURL == "" {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "heygen returned no audio_url", Final: true}
	}

	// Fetch the produced file. This is the unbounded client on purpose.
	client := c.Stream
	if client == nil {
		client = http.DefaultClient
	}
	audio, err := provider.FetchURL(ctx, client, out.Data.AudioURL, "")
	if err != nil {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Final: true,
			Message: "heygen audio download failed: " + err.Error()}
	}
	if audio.StatusCode/100 != 2 {
		provider.ReadErrorBody(audio)
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: audio.StatusCode,
			Message: "heygen audio download failed", Final: true}
	}
	ct := audio.Header.Get("Content-Type")
	if ct == "" || !strings.HasPrefix(ct, "audio/") {
		ct = contentTypes[encoding]
	}
	return &media.Audio{Body: audio.Body, ContentType: ct}, nil
}

func statusError(resp *http.Response) error {
	body := provider.ReadErrorBody(resp)
	msg := http.StatusText(resp.StatusCode)
	var d struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &d) == nil && d.Error.Message != "" {
		msg = d.Error.Message
		if d.Error.Code != "" {
			msg = d.Error.Code + ": " + msg
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
