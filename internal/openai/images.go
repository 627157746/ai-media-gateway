package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/capability"
	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type imageJSONIn struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	N              any            `json:"n"`
	Count          any            `json:"count"`
	Size           string         `json:"size"`
	Quality        string         `json:"quality"`
	Background     string         `json:"background"`
	Resolution     string         `json:"resolution"`
	ResponseFormat string         `json:"response_format"`
	Extra          map[string]any `json:"extra"`
	Images         any            `json:"images"`
	Image          any            `json:"image"`
	Mask           any            `json:"mask"`
}

func (in *imageJSONIn) toRequest(edit bool) (media.ImageRequest, error) {
	n := in.N
	if n == nil {
		n = in.Count
	}
	cnt, err := parseN(n, 1)
	if err != nil {
		return media.ImageRequest{}, err
	}
	if in.Prompt == "" {
		return media.ImageRequest{}, bad("prompt is required")
	}
	req := media.ImageRequest{
		Prompt:         in.Prompt,
		N:              cnt,
		Size:           in.Size,
		Quality:        in.Quality,
		Background:     in.Background,
		Resolution:     in.Resolution,
		ResponseFormat: in.ResponseFormat,
		Edit:           edit,
		Extra:          in.Extra,
	}
	src := in.Images
	if src == nil {
		src = in.Image
	}
	imgs, err := inputValues(src)
	if err != nil {
		return media.ImageRequest{}, err
	}
	req.Images = imgs
	if in.Mask != nil {
		mask, err := parseInputValue(in.Mask)
		if err != nil {
			return media.ImageRequest{}, err
		}
		req.Mask = &mask
	}
	return req, nil
}

func (h *handler) generateImage(w http.ResponseWriter, r *http.Request) {
	var in imageJSONIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Fail(w, r, bad("invalid JSON body: %v", err))
		return
	}
	if in.Model == "" {
		httpx.Fail(w, r, bad("model is required"))
		return
	}
	req, err := in.toRequest(false)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	logParams(r, imageParams(req))
	h.runImage(w, r, in.Model, req)
}

func (h *handler) editImage(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	var in imageJSONIn
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			httpx.Fail(w, r, bad("invalid multipart form: %v", err))
			return
		}
		fv := func(k string) string { return r.FormValue(k) }
		in.Model = fv("model")
		in.Prompt = fv("prompt")
		in.Size = fv("size")
		in.Quality = fv("quality")
		in.Background = fv("background")
		in.Resolution = fv("resolution")
		in.ResponseFormat = fv("response_format")
		if n := fv("n"); n != "" {
			in.N = n
		} else if c := fv("count"); c != "" {
			in.Count = c
		}
		if x := fv("extra"); x != "" {
			var extra map[string]any
			if err := json.Unmarshal([]byte(x), &extra); err != nil {
				httpx.Fail(w, r, bad("extra must be a JSON object"))
				return
			}
			in.Extra = extra
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			httpx.Fail(w, r, bad("invalid JSON body: %v", err))
			return
		}
	}
	if in.Model == "" {
		httpx.Fail(w, r, bad("model is required"))
		return
	}
	req, err := in.toRequest(true)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if strings.HasPrefix(ct, "multipart/form-data") {
		imgs, err := collectFiles(r, "image", "image[]")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		req.Images = imgs
		if mask, err := firstFile(r, "mask"); err != nil {
			httpx.Fail(w, r, err)
			return
		} else if mask != nil {
			req.Mask = mask
		}
	}
	logParams(r, imageParams(req))
	h.runImage(w, r, in.Model, req)
}

func (h *handler) runImage(w http.ResponseWriter, r *http.Request, modelID string, req media.ImageRequest) {
	res, err := accounts.Resolve(r.Context(), h.st, modelID, media.KindImage)
	logFill(r, modelID, &res, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	ad, err := adapter(res, "image")
	if err != nil {
		logFill(r, modelID, &res, nil)
		httpx.Fail(w, r, err)
		return
	}
	gen := ad.(provider.ImageGenerator)
	var result *media.ImageResult
	var stream *http.Client
	pr, err := h.pool.Do(r.Context(), res, func(ctx context.Context, c provider.Call) error {
		out, err := gen.GenerateImage(ctx, c, req)
		if err == nil {
			result = out
			stream = c.Stream
		}
		return err
	})
	logFill(r, modelID, &res, &pr)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	data := make([]map[string]any, 0, len(result.Images))
	wantB64 := req.ResponseFormat == "b64_json"
	for _, a := range result.Images {
		if a.B64 != "" {
			data = append(data, map[string]any{"b64_json": a.B64})
			continue
		}
		if wantB64 {
			b64, err := h.fetchB64(r, a.URL, stream)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			data = append(data, map[string]any{"b64_json": b64})
			continue
		}
		data = append(data, map[string]any{"url": a.URL})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"created": h.now().Unix(),
		"data":    data,
	})
}

func (h *handler) fetchB64(r *http.Request, u string, client *http.Client) (string, error) {
	resp, err := provider.FetchURL(r.Context(), client, u, "")
	if err != nil {
		return "", provider.TransportError(err, false)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode, Message: "image download failed"}
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// listModels implements GET /v1/models.
func (h *handler) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.st.ListModels(r.Context(), "")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	data := []map[string]any{}
	for _, m := range models {
		if !m.Enabled {
			continue
		}
		prov, err := h.st.GetProvider(r.Context(), m.ProviderID)
		if err != nil || !prov.Enabled {
			continue
		}
		entry := map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  m.CreatedAt.Unix(),
			"owned_by": m.ProviderID,
			"kind":     string(m.Kind),
		}
		// Declare what the model accepts so clients can render the right inputs
		// without hardcoding per-provider rules.
		switch m.Kind {
		case media.KindImage:
			// Prefer the vendor's own endpoint schema; it is the only source that
			// stays correct as models are added, since each endpoint has its own
			// parameters. The hand-declared table is the fallback.
			entry["capabilities"] = capability.ImageFromSchemaForKinds(
				h.imageSchema(r.Context(), m, prov),
				prov.Type, m.Upstream, m.Options)
		case media.KindVideo:
			// Video capabilities come from the endpoint schema when the vendor
			// publishes one, because the two modes (first-frame animation vs
			// identity/style references) live on different endpoints.
			var vs provider.EndpointSchema
			if raw := h.videoSchema(r.Context(), m, prov); raw != nil {
				vs = *raw
			}
			entry["capabilities"] = capability.VideoFromSchema(vs, prov.Type, m.Upstream, m.Options)
		case media.KindSpeech:
			entry["capabilities"] = capability.FromOptions(prov.Type, m.Options)
		}
		data = append(data, entry)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}
