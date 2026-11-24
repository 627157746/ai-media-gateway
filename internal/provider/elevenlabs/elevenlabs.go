// Package elevenlabs implements the ElevenLabs text-to-speech adapter.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type Adapter struct{}

func (Adapter) Type() string { return "elevenlabs" }

func init() { provider.Register("elevenlabs", Adapter{}) }

const defaultBase = "https://api.elevenlabs.io"

var formatMap = map[string]string{
	"":     "mp3_44100_128",
	"mp3":  "mp3_44100_128",
	"opus": "opus_48000_128",
	"pcm":  "pcm_24000",
	"wav":  "wav_44100",
}

var contentTypes = map[string]string{
	"mp3_44100_128":  "audio/mpeg",
	"opus_48000_128": "audio/ogg",
	"pcm_24000":      "audio/pcm",
	"wav_44100":      "audio/wav",
}

func invalid(msg string) *media.UpstreamError {
	return &media.UpstreamError{Class: media.ClassInvalid, Message: msg, Final: true}
}

func (Adapter) Speak(ctx context.Context, c provider.Call, r media.SpeechRequest) (*media.Audio, error) {
	voice := provider.ResolveVoice(c, r.Voice)
	if voice == "" {
		// Nothing configured: fall back to the first voice in the account's
		// own library so the adapter works out of the box. An explicit voice
		// (including OpenAI names such as "alloy") is still passed through
		// untouched — only an empty voice triggers this lookup.
		v, err := firstVoice(ctx, c)
		if err != nil {
			return nil, err
		}
		if v == "" {
			return nil, invalid("voice is required for ElevenLabs: set voice, configure default_voice, or add a voice to the API key's library")
		}
		voice = v
	}
	format, ok := formatMap[strings.ToLower(r.Format)]
	if !ok {
		return nil, invalid("unsupported response_format " + r.Format + " (mp3|opus|pcm|wav)")
	}
	u := c.BaseURL(defaultBase) + "/v1/text-to-speech/" + url.PathEscape(voice) +
		"?output_format=" + format

	body := map[string]any{
		"text":     r.Input,
		"model_id": c.Model.Upstream,
	}
	for k, v := range r.Extra {
		body[k] = v
	}
	if r.Speed > 0 {
		speed := r.Speed
		if speed < 0.7 {
			speed = 0.7
		}
		if speed > 1.2 {
			speed = 1.2
		}
		vs, _ := body["voice_settings"].(map[string]any)
		if vs == nil {
			vs = map[string]any{}
		}
		vs["speed"] = speed
		body["voice_settings"] = vs
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, invalid("cannot marshal request: " + err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.Account.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, true)
	}
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = contentTypes[format]
	}
	return &media.Audio{Body: resp.Body, ContentType: ct}, nil
}

type elModel struct {
	ModelID           string `json:"model_id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	CanDoTextToSpeech bool   `json:"can_do_text_to_speech"`
}

// ListModels returns the account's TTS-capable models (GET /v1/models).
func (Adapter) ListModels(ctx context.Context, c provider.Call, q provider.ModelQuery) (provider.ModelPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL(defaultBase)+"/v1/models", nil)
	if err != nil {
		return provider.ModelPage{}, err
	}
	req.Header.Set("xi-api-key", c.Account.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return provider.ModelPage{}, provider.TransportError(err, false)
	}
	if resp.StatusCode/100 != 2 {
		return provider.ModelPage{}, statusError(resp)
	}
	var list []elModel
	err = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if err != nil {
		return provider.ModelPage{}, &media.UpstreamError{
			Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "bad models response: " + err.Error(),
		}
	}
	ql := strings.ToLower(q.Q)
	out := provider.ModelPage{Models: []provider.UpstreamModel{}}
	for _, m := range list {
		if !m.CanDoTextToSpeech {
			continue
		}
		if ql != "" && !strings.Contains(strings.ToLower(m.ModelID), ql) && !strings.Contains(strings.ToLower(m.Name), ql) {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ModelID
		}
		out.Models = append(out.Models, provider.UpstreamModel{
			ID: m.ModelID, Name: name, Kind: media.KindSpeech,
			Category: "text-to-speech", Description: m.Description,
		})
	}
	return out, nil
}

// voiceCacheTTL bounds how long an auto-picked default voice is reused. The
// id is per base URL + account, so a changed key or endpoint re-resolves.
const voiceCacheTTL = 10 * time.Minute

type voiceCacheEntry struct {
	id      string
	expires time.Time
}

var defaultVoiceCache sync.Map // "base|accountID" → voiceCacheEntry

// firstVoice returns the first voice_id in the account's ElevenLabs library
// (GET /v1/voices), or "" when the library is empty. Results are cached; an
// empty library is not cached so newly created voices are picked up at once.
func firstVoice(ctx context.Context, c provider.Call) (string, error) {
	key := c.BaseURL(defaultBase) + "|" + c.Account.ID
	if v, ok := defaultVoiceCache.Load(key); ok {
		if e := v.(voiceCacheEntry); time.Now().Before(e.expires) {
			return e.id, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL(defaultBase)+"/v1/voices", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("xi-api-key", c.Account.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", provider.TransportError(err, false)
	}
	if resp.StatusCode/100 != 2 {
		return "", statusError(resp)
	}
	var out struct {
		Voices []struct {
			VoiceID string `json:"voice_id"`
		} `json:"voices"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if err != nil {
		return "", &media.UpstreamError{
			Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "bad voices response: " + err.Error(),
		}
	}
	if len(out.Voices) == 0 {
		return "", nil
	}
	id := out.Voices[0].VoiceID
	defaultVoiceCache.Store(key, voiceCacheEntry{id: id, expires: time.Now().Add(voiceCacheTTL)})
	return id, nil
}

// statusError maps ElevenLabs errors; detail may be {status,message}, a
// string, or an array.
func statusError(resp *http.Response) error {
	body := provider.ReadErrorBody(resp)
	detailStatus, msg := parseDetail(body)
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	ue := &media.UpstreamError{Status: resp.StatusCode, Message: msg}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		if detailStatus == "quota_exceeded" {
			ue.Class = media.ClassQuota
		} else {
			ue.Class = media.ClassAuth
		}
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

func parseDetail(body []byte) (status, msg string) {
	var d struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &d) != nil || len(d.Detail) == 0 {
		return "", ""
	}
	var s string
	if json.Unmarshal(d.Detail, &s) == nil {
		return "", s
	}
	var obj struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if json.Unmarshal(d.Detail, &obj) == nil && (obj.Status != "" || obj.Message != "") {
		return obj.Status, obj.Message
	}
	var arr []struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal(d.Detail, &arr) == nil && len(arr) > 0 {
		parts := make([]string, len(arr))
		for i, e := range arr {
			parts[i] = e.Msg
		}
		return "", strings.Join(parts, "; ")
	}
	return "", string(d.Detail)
}
