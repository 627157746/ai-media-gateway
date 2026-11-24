package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type speechJSONIn struct {
	Model          string         `json:"model"`
	Input          string         `json:"input"`
	Voice          string         `json:"voice"`
	ResponseFormat string         `json:"response_format"`
	Speed          float64        `json:"speed"`
	Instructions   string         `json:"instructions"`
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
	ad, err := adapter(res, "speech")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sp := ad.(provider.SpeechGenerator)
	sreq := media.SpeechRequest{
		Input:        in.Input,
		Voice:        in.Voice,
		Format:       in.ResponseFormat,
		Instructions: in.Instructions,
		Speed:        in.Speed,
		Extra:        in.Extra,
	}
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
