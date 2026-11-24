package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/store"
)

type videoJSONIn struct {
	Model         string         `json:"model"`
	Prompt        string         `json:"prompt"`
	Seconds       any            `json:"seconds"`
	Size          string         `json:"size"`
	Resolution    string         `json:"resolution"`
	ResolutionAlt string         `json:"resolution_name"`
	Ratio         string         `json:"ratio"`
	AspectRatio   string         `json:"aspect_ratio"`
	GenerateAudio any            `json:"generate_audio"`
	Watermark     any            `json:"watermark"`
	Mode          string         `json:"mode"`
	Extra         map[string]any `json:"extra"`
	Images        any            `json:"images"`
	Image         any            `json:"image"`
	FirstFrame    any            `json:"first_frame"`
	LastFrame     any            `json:"last_frame"`
	InputRef      any            `json:"input_reference"`
	Videos        any            `json:"videos"`
	Audios        any            `json:"audios"`
}

func boolField(v any) (*bool, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case bool:
		return &t, nil
	case string:
		return parseBoolPtr(t)
	default:
		return nil, bad("invalid boolean value")
	}
}

func (in *videoJSONIn) toRequest() (media.VideoRequest, error) {
	ga, err := boolField(in.GenerateAudio)
	if err != nil {
		return media.VideoRequest{}, err
	}
	wm, err := boolField(in.Watermark)
	if err != nil {
		return media.VideoRequest{}, err
	}
	resolution := in.Resolution
	if resolution == "" {
		resolution = in.ResolutionAlt
	}
	ratio := in.Ratio
	if ratio == "" {
		ratio = in.AspectRatio
	}
	return media.VideoRequest{
		Prompt:        in.Prompt,
		Mode:          in.Mode,
		Seconds:       strOf(in.Seconds),
		Size:          in.Size,
		Resolution:    resolution,
		Ratio:         ratio,
		GenerateAudio: ga,
		Watermark:     wm,
		Extra:         in.Extra,
	}, nil
}

// normalizeMode applies the mode inference rules.
func normalizeMode(r *media.VideoRequest) {
	moveRefsToFrames := func() {
		if len(r.References) > 0 && r.FirstFrame == nil {
			ff := r.References[0]
			r.FirstFrame = &ff
		}
		if len(r.References) > 1 && r.LastFrame == nil {
			lf := r.References[1]
			r.LastFrame = &lf
		}
		r.References = nil
	}
	switch r.Mode {
	case "frames":
		if r.FirstFrame == nil && r.LastFrame == nil {
			moveRefsToFrames()
		}
	case "reference":
		refs := r.References
		if r.FirstFrame != nil {
			refs = append([]media.Input{*r.FirstFrame}, refs...)
			r.FirstFrame = nil
		}
		if r.LastFrame != nil {
			refs = append(refs, *r.LastFrame)
			r.LastFrame = nil
		}
		r.References = refs
	case "":
		switch {
		case r.FirstFrame != nil || r.LastFrame != nil:
			r.Mode = "frames"
		case len(r.References) > 2:
			r.Mode = "reference"
		case len(r.References) > 0:
			r.Mode = "frames"
			moveRefsToFrames()
		}
	}
}

func (h *handler) createVideo(w http.ResponseWriter, r *http.Request) {
	var in videoJSONIn
	ct := r.Header.Get("Content-Type")
	isMultipart := len(ct) >= 19 && ct[:19] == "multipart/form-data"
	if isMultipart {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			httpx.Fail(w, r, bad("invalid multipart form: %v", err))
			return
		}
		fv := func(k string) string { return r.FormValue(k) }
		in.Model = fv("model")
		in.Prompt = fv("prompt")
		in.Seconds = fv("seconds")
		in.Size = fv("size")
		in.Resolution = fv("resolution")
		in.ResolutionAlt = fv("resolution_name")
		in.Ratio = fv("ratio")
		in.AspectRatio = fv("aspect_ratio")
		in.GenerateAudio = fv("generate_audio")
		in.Watermark = fv("watermark")
		in.Mode = fv("mode")
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
	vreq, err := in.toRequest()
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if in.Prompt == "" {
		httpx.Fail(w, r, bad("prompt is required"))
		return
	}

	if isMultipart {
		put := func(dst **media.Input, names ...string) error {
			f, err := firstFile(r, names...)
			if err != nil {
				return err
			}
			if f != nil && *dst == nil {
				*dst = f
			}
			return nil
		}
		if err := put(&vreq.FirstFrame, "first_frame", "input_reference"); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if err := put(&vreq.LastFrame, "last_frame"); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		refs, err := collectFiles(r, "image", "image[]")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		vreq.References = refs
		vids, err := collectFiles(r, "video", "video[]")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		vreq.Videos = vids
		auds, err := collectFiles(r, "audio", "audio[]")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		vreq.Audios = auds
	} else {
		set := func(dst **media.Input, v any) error {
			if v == nil {
				return nil
			}
			in2, err := parseInputValue(v)
			if err != nil {
				return err
			}
			*dst = &in2
			return nil
		}
		src := in.Images
		if src == nil {
			src = in.Image
		}
		refs, err := inputValues(src)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		vreq.References = refs
		if err := set(&vreq.FirstFrame, in.FirstFrame); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if err := set(&vreq.LastFrame, in.LastFrame); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if vreq.FirstFrame == nil {
			if err := set(&vreq.FirstFrame, in.InputRef); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		vids, err := inputValues(in.Videos)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		vreq.Videos = vids
		auds, err := inputValues(in.Audios)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		vreq.Audios = auds
	}
	normalizeMode(&vreq)
	logParams(r, videoParams(vreq))

	res, err := accounts.Resolve(r.Context(), h.st, in.Model, media.KindVideo)
	logFill(r, in.Model, &res, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	ad, err := adapter(res, "video")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	vg := ad.(provider.VideoGenerator)
	var handle media.TaskHandle
	pr, err := h.pool.Do(r.Context(), res, func(ctx context.Context, c provider.Call) error {
		th, err := vg.SubmitVideo(ctx, c, vreq)
		if err == nil {
			handle = th
		}
		return err
	})
	logFill(r, in.Model, &res, &pr)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	task := &store.VideoTask{
		ID:         "video_" + store.RandHex(12),
		ModelID:    in.Model,
		ProviderID: res.Provider.ID,
		AccountID:  pr.AccountID,
		Handle:     handle,
		Status:     media.TaskQueued,
	}
	if err := h.st.CreateVideoTask(r.Context(), task); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, videoObject(task))
}

func videoObject(t *store.VideoTask) map[string]any {
	obj := map[string]any{
		"id":           t.ID,
		"object":       "video",
		"model":        t.ModelID,
		"status":       string(t.Status),
		"progress":     t.Progress,
		"created_at":   t.CreatedAt.Unix(),
		"completed_at": nil,
		"error":        nil,
	}
	if t.CompletedAt != nil {
		obj["completed_at"] = t.CompletedAt.Unix()
	}
	if t.Status == media.TaskCompleted && t.ResultURL != "" {
		obj["video_url"] = t.ResultURL
	}
	if t.Error != "" {
		obj["error"] = map[string]any{"message": t.Error, "code": "video_error"}
	}
	return obj
}

func terminal(s media.TaskStatus) bool {
	return s == media.TaskCompleted || s == media.TaskFailed || s == media.TaskCancelled
}

func (h *handler) getVideo(w http.ResponseWriter, r *http.Request) {
	t, err := h.st.GetVideoTask(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if terminal(t.Status) ||
		(t.LastPolledAt != nil && h.now().Sub(*t.LastPolledAt) < time.Second) {
		httpx.WriteJSON(w, http.StatusOK, videoObject(t))
		return
	}
	h.pollTask(w, r, t)
}

func (h *handler) pollTask(w http.ResponseWriter, r *http.Request, t *store.VideoTask) {
	now := h.now()
	t.LastPolledAt = &now

	fail := func(msg string) {
		t.Status = media.TaskFailed
		t.Error = msg
		t.CompletedAt = &now
		_ = h.st.UpdateVideoTask(r.Context(), t)
		httpx.WriteJSON(w, http.StatusOK, videoObject(t))
	}
	keep := func(err error) {
		slog.Warn("video poll failed, keeping state", "task", t.ID, "err", err)
		_ = h.st.UpdateVideoTask(r.Context(), t)
		httpx.WriteJSON(w, http.StatusOK, videoObject(t))
	}

	res, err := h.taskResolved(r.Context(), t)
	if err != nil {
		fail("provider " + t.ProviderID + " was deleted")
		return
	}
	call, err := h.pool.CallFor(r.Context(), res, t.AccountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail("account " + t.AccountID + " was deleted")
		} else {
			fail(err.Error())
		}
		return
	}
	ad, err := adapter(res, "video")
	if err != nil {
		fail(err.Error())
		return
	}
	state, err := ad.(provider.VideoGenerator).PollVideo(r.Context(), call, t.Handle)
	if err != nil {
		var ue *media.UpstreamError
		if errors.As(err, &ue) && ue.Class.Failover() && !ue.Final {
			// transient vendor failure: keep state, try again next poll
			keep(err)
		} else {
			fail(err.Error())
		}
		return
	}
	t.Status = state.Status
	t.Progress = state.Progress
	switch state.Status {
	case media.TaskCompleted:
		t.ResultURL = state.URL
		t.CompletedAt = &now
	case media.TaskFailed, media.TaskCancelled:
		t.Error = state.Error
		t.CompletedAt = &now
	}
	if err := h.st.UpdateVideoTask(r.Context(), t); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, videoObject(t))
}

// taskResolved rebuilds the Resolved for a stored video task — the provider
// must still exist; the model lookup is best-effort (deleted → upstream=id).
func (h *handler) taskResolved(ctx context.Context, t *store.VideoTask) (accounts.Resolved, error) {
	prov, err := h.st.GetProvider(ctx, t.ProviderID)
	if err != nil {
		return accounts.Resolved{}, err
	}
	mcfg := provider.ModelConfig{ID: t.ModelID, Kind: media.KindVideo, Upstream: t.ModelID}
	if m, err := h.st.GetModel(ctx, t.ModelID); err == nil {
		mcfg = provider.ModelConfig{ID: m.ID, Kind: m.Kind, Upstream: m.Upstream, Options: m.Options}
	}
	return accounts.Resolved{
		Provider: provider.ProviderConfig{ID: prov.ID, Type: prov.Type, BaseURL: prov.BaseURL, ProxyURL: prov.ProxyURL, Options: prov.Options},
		Model:    mcfg,
	}, nil
}

func (h *handler) videoContent(w http.ResponseWriter, r *http.Request) {
	t, err := h.st.GetVideoTask(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if t.Status != media.TaskCompleted || t.ResultURL == "" {
		httpx.Error(w, http.StatusConflict, "video is not ready", "invalid_request_error", "video_not_ready")
		return
	}
	// Download through the task's bound account proxy when resolvable;
	// otherwise the global proxy (account/provider may be gone by now).
	var client *http.Client
	var res accounts.Resolved
	var haveRes bool
	var call provider.Call
	var callErr error
	if r0, err := h.taskResolved(r.Context(), t); err == nil {
		res, haveRes = r0, true
		if call, callErr = h.pool.CallFor(r.Context(), res, t.AccountID); callErr == nil {
			client = call.Stream
		}
	}

	// An adapter whose vendor needs authentication for the result fetches the
	// bytes itself. Muse, for example, gates /jobs/<id>/result behind an API key
	// that a bare GET would never send.
	if haveRes {
		if ad, ok := provider.Get(res.Provider.Type); ok {
			if fetcher, ok := ad.(provider.VideoFetcher); ok {
				if callErr != nil {
					h.failContent(w, r, callErr)
					return
				}
				resp, ferr := fetcher.FetchVideo(r.Context(), call, t.ResultURL, r.Header.Get("Range"))
				if ferr != nil {
					h.failContent(w, r, ferr)
					return
				}
				defer resp.Body.Close()
				for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
					if v := resp.Header.Get(k); v != "" {
						w.Header().Set(k, v)
					}
				}
				w.WriteHeader(resp.StatusCode)
				_, _ = io.Copy(w, resp.Body)
				return
			}
		}
	}

	if client == nil {
		client, err = h.pool.StreamForGlobal(r.Context())
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	resp, err := provider.FetchURL(r.Context(), client, t.ResultURL, r.Header.Get("Range"))
	if err != nil {
		httpx.Fail(w, r, provider.TransportError(err, false))
		return
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// failContent reports a download failure while streaming a finished video.
// A vendor error keeps its class (an expired Muse key is 502 upstream_auth, a
// missing job 400), instead of being flattened into a generic transport error.
func (h *handler) failContent(w http.ResponseWriter, r *http.Request, err error) {
	var ue *media.UpstreamError
	if errors.As(err, &ue) {
		httpx.Fail(w, r, ue)
		return
	}
	httpx.Fail(w, r, provider.TransportError(err, false))
}

func (h *handler) listVideos(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	tasks, err := h.st.ListVideoTasks(r.Context(), "", limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	data := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		data = append(data, videoObject(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}
