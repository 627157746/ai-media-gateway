package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/capability"
	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/store"
)

type speechJSONIn struct {
	Model          string         `json:"model"`
	Input          string         `json:"input"`
	Voice          string         `json:"voice"`
	ResponseFormat string         `json:"response_format"`
	Speed          float64        `json:"speed"`
	Instructions   string         `json:"instructions"`
	Language       string         `json:"language"`
	Extra          map[string]any `json:"extra"`
}

func (h *handler) speech(w http.ResponseWriter, r *http.Request) {
	var in speechJSONIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Fail(w, r, bad("invalid JSON body: %v", err))
		return
	}
	if in.Model == "" {
		httpx.Fail(w, r, bad("model is required"))
		return
	}
	if in.Input == "" {
		httpx.Fail(w, r, bad("input is required"))
		return
	}
	res, err := accounts.Resolve(r.Context(), h.st, in.Model, media.KindSpeech)
	logFill(r, in.Model, &res, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	// Resolve a 音色库 id to the vendor voice id. Must happen after Resolve
	// (we need the provider) and before Pool.Do (the request carried to the
	// adapter must hold the final id). VoiceResolved stops the adapters from
	// running ResolveVoice over an already-mapped id.
	voiceResolved := false
	var refAudio *media.Input
	if in.Voice != "" {
		v, err := h.st.FindVoice(r.Context(), res.Provider.ID, in.Voice)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		switch {
		case v == nil && strings.HasPrefix(in.Voice, "vc_"):
			// A vc_* id that does not resolve for this provider is almost always a
			// voice from another platform's library. Saying so beats forwarding the
			// id upstream, where it surfaces as an opaque "Reference not found".
			httpx.Fail(w, r, h.voiceWrongProvider(r.Context(), res.Provider.ID, in.Voice))
			return
		case v != nil && v.Kind == store.KindSample:
			// Zero-shot cloning: load the stored sample and let the adapter embed
			// it. The id is not forwarded upstream at all.
			sample, err := h.readVoiceSample(v.UpstreamID)
			if err != nil {
				httpx.Fail(w, r, bad("音色 %q 的参考音频不可用：%v", v.Name, err))
				return
			}
			refAudio = sample
			in.Voice = ""
			voiceResolved = true
		case v != nil:
			in.Voice = v.UpstreamID
			voiceResolved = true
		}
	}
	spec := capability.FromOptions(res.Provider.Type, res.Model.Options)
	if err := h.checkVoice(r.Context(), res, spec, in.Voice, voiceResolved, refAudio != nil); err != nil {
		logParams(r, map[string]any{"voice": in.Voice})
		httpx.Fail(w, r, err)
		return
	}

	sreq := media.SpeechRequest{
		Input:          in.Input,
		Voice:          in.Voice,
		VoiceResolved:  voiceResolved,
		Format:         in.ResponseFormat,
		Instructions:   in.Instructions,
		Speed:          in.Speed,
		Extra:          in.Extra,
		ReferenceAudio: refAudio,
	}
	logParams(r, speechParams(sreq))
	ad, err := adapter(res, "speech")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if h.dryRun(w, r) {
		return
	}
	sp := ad.(provider.SpeechGenerator)
	var audio *media.Audio
	pr, err := h.pool.Do(r.Context(), res, func(ctx context.Context, c provider.Call) error {
		out, err := sp.Speak(ctx, c, sreq)
		if err == nil {
			audio = out
		}
		return err
	})
	logFill(r, in.Model, &res, &pr)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	defer audio.Body.Close()

	ct := audio.ContentType
	if ct == "" {
		ct = "audio/mpeg"
	}
	w.Header().Set("Content-Type", ct)
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := audio.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
	}
}
