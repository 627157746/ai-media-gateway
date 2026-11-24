// Package elevenlabs implements the ElevenLabs text-to-speech adapter.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-media-gateway/internal/capability"
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
	voice := r.Voice
	if !r.VoiceResolved {
		voice = provider.ResolveVoice(c, voice)
	}
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
	// language_code exists in the API, but multilingual v2 rejects it. Sending
	// it only when configured keeps both cases working.
	if r.Language != "" {
		body["language_code"] = r.Language
	}
	for k, v := range r.Extra {
		body[k] = v
	}
	if speed := capability.For("elevenlabs").Speed.Clamp(r.Speed); speed > 0 {
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

// voiceListEntry caches the full voice library for the 从上游导入 picker.
type voiceListEntry struct {
	voices  []provider.UpstreamVoice
	expires time.Time
}

var voiceListCache sync.Map // "base|accountID" → voiceListEntry

// CloneVoice creates a durable IVC voice (POST /v1/voices/add). The request is
// multipart and the boundary must be set by the client, so Content-Type is
// never assigned by hand.
func (Adapter) CloneVoice(ctx context.Context, c provider.Call, r provider.CloneRequest) (*provider.CloneResult, error) {
	if len(r.Files) == 0 {
		return nil, invalid("至少需要一个音频样本")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("name", r.Name); err != nil {
		return nil, err
	}
	if r.Description != "" {
		if err := mw.WriteField("description", r.Description); err != nil {
			return nil, err
		}
	}
	if r.RemoveBackgroundNoise {
		if err := mw.WriteField("remove_background_noise", "true"); err != nil {
			return nil, err
		}
	}
	for i, data := range r.Files {
		part, err := mw.CreateFormFile("files", fmt.Sprintf("sample-%d%s", i+1, sniffAudioExt(data)))
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL(defaultBase)+"/v1/voices/add", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.Account.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, false)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	var out struct {
		VoiceID              string `json:"voice_id"`
		RequiresVerification bool   `json:"requires_verification"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &media.UpstreamError{
			Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "bad clone response: " + err.Error(),
		}
	}
	if out.VoiceID == "" {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Final: true,
			Message: "clone response has no voice_id"}
	}
	return &provider.CloneResult{VoiceID: out.VoiceID, RequiresVerification: out.RequiresVerification}, nil
}

// sniffAudioExt picks a filename extension from the file signature so the
// upload carries a believable name; the vendor sniffs the bytes anyway.
func sniffAudioExt(data []byte) string {
	switch {
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return ".wav"
	case len(data) >= 3 && string(data[0:3]) == "ID3":
		return ".mp3"
	case len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0:
		return ".mp3"
	case len(data) >= 4 && string(data[0:4]) == "OggS":
		return ".ogg"
	case len(data) >= 4 && string(data[0:4]) == "fLaC":
		return ".flac"
	default:
		return ".mp3"
	}
}

// ListVoices returns the account's ElevenLabs voice library (GET /v1/voices),
// including each voice's display name. An empty library is not cached, so a
// freshly created voice shows up on the next call.
func (Adapter) ListVoices(ctx context.Context, c provider.Call) ([]provider.UpstreamVoice, error) {
	key := c.BaseURL(defaultBase) + "|" + c.Account.ID
	if v, ok := voiceListCache.Load(key); ok {
		if e := v.(voiceListEntry); time.Now().Before(e.expires) {
			return e.voices, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL(defaultBase)+"/v1/voices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.Account.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, false)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	var out struct {
		Voices []struct {
			VoiceID  string            `json:"voice_id"`
			Name     string            `json:"name"`
			Category string            `json:"category"`
			Labels   map[string]string `json:"labels"`
		} `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &media.UpstreamError{
			Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "bad voices response: " + err.Error(),
		}
	}
	voices := make([]provider.UpstreamVoice, 0, len(out.Voices))
	for _, v := range out.Voices {
		if v.VoiceID == "" {
			continue
		}
		name := v.Name
		if name == "" {
			name = v.VoiceID
		}
		detail := v.Category
		if lang := v.Labels["language"]; lang != "" {
			detail = strings.TrimSpace(detail + " · " + lang)
		}
		voices = append(voices, provider.UpstreamVoice{ID: v.VoiceID, Name: name, Detail: detail})
	}
	if len(voices) > 0 {
		voiceListCache.Store(key, voiceListEntry{voices: voices, expires: time.Now().Add(voiceCacheTTL)})
	}
	return voices, nil
}

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
