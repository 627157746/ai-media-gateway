// Package mimo implements the Xiaomi MiMo text-to-speech adapter.
//
// MiMo speaks the OpenAI chat-completions shape and returns audio inline as
// base64 in choices[0].message.audio.data. One endpoint serves three modes,
// selected by the upstream model id:
//
//	…tts         plain synthesis, voice is a preset name (mimo_default, 冰糖…)
//	…voiceclone  voice is a reference sample (data URL / local file)
//	…voicedesign voice is omitted; a style prompt defines the timbre instead
package mimo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ai-media-gateway/internal/capability"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type Adapter struct{}

func (Adapter) Type() string { return "mimo" }

func init() { provider.Register("mimo", Adapter{}) }

const defaultBase = "https://api.xiaomimimo.com/v1"

var validFormats = map[string]string{
	"":     "mp3",
	"mp3":  "mp3",
	"wav":  "wav",
	"pcm":  "pcm",
	"opus": "mp3", // not offered upstream; fall back to mp3
}

var contentTypes = map[string]string{
	"mp3": "audio/mpeg",
	"wav": "audio/wav",
	"pcm": "audio/wav", // MiMo returns PCM inside a wav container
}

func invalid(format string, args ...any) *media.UpstreamError {
	return &media.UpstreamError{Class: media.ClassInvalid, Message: fmt.Sprintf(format, args...), Final: true}
}

func (Adapter) Speak(ctx context.Context, c provider.Call, r media.SpeechRequest) (*media.Audio, error) {
	format := strings.ToLower(r.Format)
	mimoFormat, ok := validFormats[format]
	if !ok {
		return nil, invalid("unsupported response_format %q (mp3|wav|pcm)", r.Format)
	}

	modelID := c.Model.Upstream
	isClone := strings.Contains(modelID, "voiceclone")
	isDesign := strings.Contains(modelID, "voicedesign")

	// MiMo has no speed field: rate is steered by natural language in the user
	// turn, so a requested speed becomes part of the style prompt. This is the
	// documented mechanism, not a workaround.
	style := r.Instructions
	if speedHint := capability.For("mimo").Speed.SpeedPhrase(r.Speed); speedHint != "" {
		if style == "" {
			style = speedHint
		} else {
			style = style + "，" + speedHint
		}
	}
	if style == "" {
		style = "自然流畅，清晰传神"
	}

	audio := map[string]any{"format": mimoFormat}
	body := map[string]any{
		"model": modelID,
		"messages": []any{
			map[string]any{"role": "user", "content": style},
			map[string]any{"role": "assistant", "content": r.Input},
		},
		"audio": audio,
	}

	switch {
	case isDesign:
		// A design prompt is the timbre description; it rides in the user turn
		// and no voice field is sent.
		if r.Voice != "" {
			body["messages"] = []any{
				map[string]any{"role": "user", "content": r.Voice},
				map[string]any{"role": "assistant", "content": r.Input},
			}
		}
	case isClone:
		voiceParam, err := cloneVoiceParam(c, r)
		if err != nil {
			return nil, err
		}
		audio["voice"] = voiceParam
	default:
		voice := r.Voice
		if !r.VoiceResolved {
			voice = provider.ResolveVoice(c, voice)
		}
		if voice != "" {
			audio["voice"] = voice
		} else {
			audio["voice"] = "mimo_default"
		}
	}
	for k, v := range r.Extra {
		body[k] = v
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, invalid("cannot marshal request: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL(defaultBase)+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("api-key", c.Account.APIKey)
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
		Choices []struct {
			Message struct {
				Audio struct {
					Data string `json:"data"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "bad tts response: " + err.Error(), Final: true}
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Audio.Data == "" {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "mimo response has no audio data", Final: true}
	}
	decoded, err := base64.StdEncoding.DecodeString(out.Choices[0].Message.Audio.Data)
	if err != nil {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "mimo audio is not valid base64: " + err.Error(), Final: true}
	}
	return &media.Audio{
		Body:        io.NopCloser(bytes.NewReader(decoded)),
		ContentType: contentTypes[mimoFormat],
	}, nil
}

// cloneVoiceParam turns the reference audio into the data URL MiMo expects.
// It accepts either an already-encoded data URL or raw bytes supplied by the
// caller (the gateway loads the stored sample).
func cloneVoiceParam(c provider.Call, r media.SpeechRequest) (string, error) {
	if ref := r.ReferenceAudio; ref != nil && len(ref.Data) > 0 {
		mime := ref.MIME
		if mime == "" {
			mime = "audio/mp3"
		}
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(ref.Data), nil
	}
	if strings.HasPrefix(r.Voice, "data:audio/") {
		return r.Voice, nil
	}
	voice := r.Voice
	if !r.VoiceResolved {
		voice = provider.ResolveVoice(c, voice)
	}
	if voice == "" {
		return "", invalid("模型 %s 是声音克隆模型，需要传入音频样本", c.Model.Upstream)
	}
	return "", invalid("模型 %s 需要参考音频，但 %q 不是可用的样本", c.Model.Upstream, voice)
}

func statusError(resp *http.Response) error {
	body := provider.ReadErrorBody(resp)
	msg := http.StatusText(resp.StatusCode)
	var d struct {
		Error struct {
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &d) == nil && d.Error.Message != "" {
		msg = d.Error.Message
		if d.Error.Param != "" {
			msg += " (" + d.Error.Param + ")"
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
