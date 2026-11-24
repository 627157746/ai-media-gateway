// Package fal implements the fal.ai queue adapter (image, video, speech).
package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type Adapter struct{}

func (Adapter) Type() string { return "fal" }

func init() { provider.Register("fal", Adapter{}) }

const defaultBase = "https://queue.fal.run"

// var for tests.
var pollInterval = 1500 * time.Millisecond

// modelOpts mirrors the documented model.options schema.
type modelOpts struct {
	UpstreamImage     string            `json:"upstream_image"`
	UpstreamReference string            `json:"upstream_reference"`
	UpstreamEdit      string            `json:"upstream_edit"`
	Defaults          map[string]any    `json:"defaults"`
	Fields            map[string]string `json:"fields"`
	Drop              []string          `json:"drop"`
}

func parseOpts(raw json.RawMessage) modelOpts {
	var o modelOpts
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o
}

// field returns the vendor input field name for a logical key, and whether to
// include it (an empty configured name omits the field entirely).
func (o modelOpts) field(key, def string) (string, bool) {
	if name, ok := o.Fields[key]; ok {
		return name, name != ""
	}
	return def, true
}

func (o modelOpts) finish(in map[string]any, extra map[string]any) map[string]any {
	for k, v := range extra {
		in[k] = v
	}
	for _, k := range o.Drop {
		delete(in, k)
	}
	return in
}

// ---- queue protocol ----

type handle struct {
	RequestID   string `json:"request_id"`
	StatusURL   string `json:"status_url"`
	ResponseURL string `json:"response_url"`
	CancelURL   string `json:"cancel_url"`
	Endpoint    string `json:"endpoint"`
}

func (Adapter) submit(ctx context.Context, c provider.Call, endpoint string, input map[string]any) (*handle, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", media.ErrBadRequest, err)
	}
	u := c.BaseURL(defaultBase) + "/" + strings.TrimPrefix(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Key "+c.Account.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, true)
	}
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	var h handle
	err = json.NewDecoder(resp.Body).Decode(&h)
	resp.Body.Close()
	if err != nil {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode, Message: "bad submit response: " + err.Error()}
	}
	h.Endpoint = endpoint
	return &h, nil
}

type queueStatus struct {
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
}

// pollOnce fetches the queue status. Terminal "COMPLETED" still requires a
// result fetch (fal marks failed/cancelled requests COMPLETED too).
func (Adapter) pollOnce(ctx context.Context, c provider.Call, h *handle) (queueStatus, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.StatusURL, nil)
	if err != nil {
		return queueStatus{}, nil, err
	}
	req.Header.Set("Authorization", "Key "+c.Account.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return queueStatus{}, nil, provider.TransportError(err, false)
	}
	if resp.StatusCode/100 != 2 {
		return queueStatus{}, nil, statusError(resp)
	}
	var st queueStatus
	err = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if err != nil {
		return queueStatus{}, nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode, Message: "bad status response"}
	}
	if msg := rawErrMessage(st.Error); msg != "" {
		return st, nil, &media.UpstreamError{Class: media.ClassInvalid, Status: resp.StatusCode, Message: msg, Final: true}
	}
	if st.Status != "COMPLETED" {
		return st, nil, nil
	}
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, h.ResponseURL, nil)
	if err != nil {
		return st, nil, err
	}
	req2.Header.Set("Authorization", "Key "+c.Account.APIKey)
	resp2, err := c.HTTP.Do(req2)
	if err != nil {
		return st, nil, provider.TransportError(err, false)
	}
	if resp2.StatusCode/100 != 2 {
		return st, nil, statusError(resp2)
	}
	var result json.RawMessage
	err = json.NewDecoder(resp2.Body).Decode(&result)
	resp2.Body.Close()
	if err != nil {
		return st, nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp2.StatusCode, Message: "bad result response"}
	}
	return st, result, nil
}

// pollResult polls every 1.5s until COMPLETED, timeout, or cancel. Transient
// upstream/429 status-poll failures are retried (up to 5 consecutive); a status
// response carrying "error" or any other class aborts immediately.
func (a Adapter) pollResult(ctx context.Context, c provider.Call, h *handle) (json.RawMessage, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wait := func(d time.Duration) error {
		select {
		case <-ctx.Done():
			a.cancelBestEffort(c, h)
			return a.ctxErr(parent, h, timeout)
		case <-time.After(d):
			return nil
		}
	}
	consecutive := 0
	for {
		_, result, err := a.pollOnce(ctx, c, h)
		if err != nil {
			if ctx.Err() != nil {
				a.cancelBestEffort(c, h)
				return nil, a.ctxErr(parent, h, timeout)
			}
			if consecutive++; consecutive < 5 {
				var ue *media.UpstreamError
				if errors.As(err, &ue) && !ue.Final && (ue.Class == media.ClassUpstream || ue.Class == media.ClassRateLimit) {
					d := pollInterval
					if ue.RetryAfter > d {
						d = ue.RetryAfter
					}
					if werr := wait(d); werr != nil {
						return nil, werr
					}
					continue
				}
			}
			return nil, err
		}
		consecutive = 0
		if result != nil {
			return result, nil
		}
		if werr := wait(pollInterval); werr != nil {
			return nil, werr
		}
	}
}

// ctxErr distinguishes the adapter's own poll deadline (parent still alive →
// Final UpstreamError, status 502) from a client cancel (raw ctx error).
func (Adapter) ctxErr(parent context.Context, h *handle, timeout time.Duration) error {
	if parent.Err() == nil {
		return &media.UpstreamError{
			Class:   media.ClassUpstream,
			Final:   true,
			Message: fmt.Sprintf("fal request %s timed out after %s", h.RequestID, timeout),
		}
	}
	return parent.Err()
}

// finalErr marks a post-submit failure Final — the job may already be running
// (and billed) at fal, so failing over to another account would duplicate it.
// Context errors pass through untouched so callers still see the client cancel.
func finalErr(h *handle, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ue *media.UpstreamError
	if errors.As(err, &ue) {
		cp := *ue
		cp.Final = true
		cp.Message = withRequestID(cp.Message, h.RequestID)
		return &cp
	}
	return &media.UpstreamError{
		Class:   media.ClassUpstream,
		Final:   true,
		Message: withRequestID(err.Error(), h.RequestID),
	}
}

func withRequestID(msg, id string) string {
	if id == "" {
		return msg
	}
	return fmt.Sprintf("%s (fal request %s)", msg, id)
}

func (Adapter) cancelBestEffort(c provider.Call, h *handle) {
	if h.CancelURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, h.CancelURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Key "+c.Account.APIKey)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// ---- error mapping ----

func statusError(resp *http.Response) error {
	body := provider.ReadErrorBody(resp)
	ue := &media.UpstreamError{Status: resp.StatusCode, Message: detailMessage(body)}
	if ue.Message == "" {
		ue.Message = http.StatusText(resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		ue.Class = media.ClassAuth
	case http.StatusPaymentRequired:
		ue.Class = media.ClassQuota
	case http.StatusForbidden:
		lower := strings.ToLower(string(body))
		ue.Class = media.ClassAuth
		for _, w := range []string{"balance", "exhausted", "locked", "credit", "billing"} {
			if strings.Contains(lower, w) {
				ue.Class = media.ClassQuota
				break
			}
		}
	case http.StatusTooManyRequests:
		ue.Class = media.ClassRateLimit
		ue.RetryAfter = provider.ParseRetryAfter(resp.Header)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
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

// detailMessage extracts fal's {"detail": string|[{msg,loc}]} error body.
func detailMessage(body []byte) string {
	var d struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &d) != nil || len(d.Detail) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(d.Detail, &s) == nil {
		return s
	}
	var arr []struct {
		Msg string `json:"msg"`
		Loc []any  `json:"loc"`
	}
	if json.Unmarshal(d.Detail, &arr) == nil && len(arr) > 0 {
		parts := make([]string, len(arr))
		for i, e := range arr {
			loc := ""
			if len(e.Loc) > 0 {
				loc = fmt.Sprintf("(%s)", joinLoc(e.Loc))
			}
			parts[i] = e.Msg + loc
		}
		return strings.Join(parts, "; ")
	}
	return string(d.Detail)
}

func joinLoc(loc []any) string {
	parts := make([]string, len(loc))
	for i, p := range loc {
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, ".")
}

func rawErrMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Message != "" {
		return o.Message
	}
	return string(raw)
}

// ---- result extraction ----

func walkURLs(node any, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			if k == "url" {
				if s, ok := val.(string); ok && s != "" {
					*out = append(*out, s)
					continue
				}
			}
			walkURLs(val, out)
		}
	case []any:
		for _, item := range v {
			walkURLs(item, out)
		}
	}
}

func decodeResult(raw json.RawMessage) any {
	var v any
	_ = json.Unmarshal(raw, &v)
	return v
}

func imageURLs(result any) []string {
	if m, ok := result.(map[string]any); ok {
		var urls []string
		if arr, ok := m["images"].([]any); ok {
			walkURLs(arr, &urls)
			if len(urls) > 0 {
				return urls
			}
		}
		if img, ok := m["image"].(map[string]any); ok {
			if s, _ := img["url"].(string); s != "" {
				return []string{s}
			}
		}
	}
	return nil
}

func firstURLUnder(result any, keys ...string) string {
	if m, ok := result.(map[string]any); ok {
		for _, k := range keys {
			var urls []string
			walkURLs(m[k], &urls)
			if len(urls) > 0 {
				return urls[0]
			}
		}
	}
	var urls []string
	walkURLs(result, &urls)
	if len(urls) > 0 {
		return urls[0]
	}
	return ""
}

// ---- endpoint + input builders ----

// videoEndpoint picks the endpoint for a request. Mirrors the image adapter's
// upstream_edit handling: the request content decides, not just an explicit
// mode, because a client that supplies reference images clearly wants reference
// mode but may not set the field.
func videoEndpoint(r media.VideoRequest, o modelOpts, up string) string {
	// Reference media (images/videos/audio used as references) only exists on
	// the reference endpoint, and asking for reference mode explicitly means the
	// same thing.
	// An explicit frames mode wins over leftover reference media: the client
	// chose image-to-video, so it must not be silently repointed.
	hasRefMedia := len(r.References) > 0 || len(r.Videos) > 0 || len(r.Audios) > 0
	wantsReference := r.Mode == "reference" || (r.Mode != "frames" && hasRefMedia)
	if wantsReference && o.UpstreamReference != "" {
		return o.UpstreamReference
	}
	// Any supplied image needs the image endpoint when no reference endpoint
	// is configured (the pre-existing upstream_image contract).
	if (r.FirstFrame != nil || r.LastFrame != nil || len(r.References) > 0) && o.UpstreamImage != "" {
		return o.UpstreamImage
	}
	return up
}

var imageRatios = []string{"21:9", "16:9", "3:2", "4:3", "5:4", "1:1", "4:5", "3:4", "2:3", "9:16", "9:21"}
var videoRatios = []string{"21:9", "16:9", "4:3", "1:1", "3:4", "9:16"}

func ratioValue(r string) float64 {
	parts := strings.SplitN(r, ":", 2)
	if len(parts) != 2 {
		return 0
	}
	w, err1 := strconv.ParseFloat(parts[0], 64)
	h, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || h == 0 {
		return 0
	}
	return w / h
}

// nearestRatio maps a "WxH" or "a:b" size string to the closest listed ratio.
func nearestRatio(size string, list []string) string {
	size = strings.ToLower(strings.TrimSpace(size))
	var r float64
	if strings.Contains(size, "x") {
		parts := strings.SplitN(size, "x", 2)
		w, err1 := strconv.ParseFloat(parts[0], 64)
		h, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 == nil && err2 == nil && h != 0 {
			r = w / h
		}
	} else {
		r = ratioValue(size)
	}
	if r <= 0 {
		return ""
	}
	best, bestD := "", math.MaxFloat64
	for _, cand := range list {
		if d := math.Abs(ratioValue(cand) - r); d < bestD {
			best, bestD = cand, d
		}
	}
	return best
}

func buildImageInput(r media.ImageRequest, o modelOpts) map[string]any {
	in := map[string]any{}
	for k, v := range o.Defaults {
		in[k] = v
	}
	set := func(key, def string, v any) {
		if name, ok := o.field(key, def); ok {
			in[name] = v
		}
	}
	set("prompt", "prompt", r.Prompt)
	if r.N > 0 {
		set("count", "num_images", r.N)
	}
	// size handling
	mode := "image_size"
	if m, ok := o.Fields["size_mode"]; ok {
		mode = m
	}
	size := strings.TrimSpace(r.Size)
	if mode != "none" && size != "" && !strings.EqualFold(size, "auto") {
		if mode == "aspect_ratio" {
			if near := nearestRatio(size, imageRatios); near != "" {
				set("ratio", "aspect_ratio", near)
			}
		} else if i := strings.Index(size, "x"); i > 0 {
			w, err1 := strconv.Atoi(size[:i])
			h, err2 := strconv.Atoi(size[i+1:])
			if err1 == nil && err2 == nil {
				set("size", "image_size", map[string]any{"width": w, "height": h})
			} else {
				set("size", "image_size", size)
			}
		} else if ratioValue(size) > 0 {
			set("ratio", "aspect_ratio", size)
		} else {
			set("size", "image_size", size)
		}
	}
	if r.Resolution != "" {
		set("resolution", "resolution", r.Resolution)
	}
	if r.Quality != "" {
		if name, ok := o.Fields["quality"]; ok && name != "" {
			in[name] = r.Quality
		}
	}
	if r.Background != "" {
		if name, ok := o.Fields["background"]; ok && name != "" {
			in[name] = r.Background
		}
	}
	// response_format carries OpenAI semantics (png/jpeg/webp, or url/b64_json
	// to pick the response shape). fal's field is output_format and accepts only
	// real formats, so the two response-shape markers are not forwarded.
	if f := strings.TrimSpace(r.ResponseFormat); f != "" && f != "url" && f != "b64_json" {
		set("format", "output_format", f)
	}
	if len(r.Images) > 0 {
		urls := make([]string, len(r.Images))
		for i, img := range r.Images {
			urls[i] = img.DataURL()
		}
		set("images", "image_urls", urls)
	}
	if r.Mask != nil {
		set("mask", "mask_url", r.Mask.DataURL())
	}
	return o.finish(in, r.Extra)
}

func durationValue(seconds string, format string) any {
	switch format {
	case "int":
		if n, err := strconv.Atoi(strings.TrimSuffix(seconds, "s")); err == nil {
			return n
		}
		return seconds
	case "{n}s":
		return strings.TrimSuffix(seconds, "s") + "s"
	default: // "string"
		return seconds
	}
}

func buildVideoInput(r media.VideoRequest, o modelOpts, endpoint string) map[string]any {
	in := map[string]any{}
	for k, v := range o.Defaults {
		in[k] = v
	}
	set := func(key, def string, v any) {
		if name, ok := o.field(key, def); ok {
			in[name] = v
		}
	}
	set("prompt", "prompt", r.Prompt)
	if r.Seconds != "" {
		format := "string"
		if f, ok := o.Fields["duration_format"]; ok {
			format = f
		}
		set("duration", "duration", durationValue(r.Seconds, format))
	}
	size := r.Ratio
	if size == "" {
		size = r.Size
	}
	if size != "" && !strings.EqualFold(size, "auto") {
		if near := nearestRatio(size, videoRatios); near != "" {
			set("ratio", "aspect_ratio", near)
		}
	}
	if r.Resolution != "" {
		set("resolution", "resolution", strings.ToLower(r.Resolution))
	}
	if r.GenerateAudio != nil {
		set("generate_audio", "generate_audio", *r.GenerateAudio)
	}
	if r.Watermark != nil {
		if name, ok := o.Fields["watermark"]; ok && name != "" {
			in[name] = *r.Watermark
		}
	}
	// The first-frame field name differs by vendor: MiniMax uses image_url,
	// Alibaba uses start_image_url. Rather than hardcoding vendors here, the
	// model's options.fields.first_frame names it (see the admin model entry);
	// image_url is only the fallback. Sending the wrong name is silently ignored
	// upstream, which looks like the frame was never applied.
	if r.FirstFrame != nil {
		set("first_frame", "image_url", r.FirstFrame.DataURL())
	}
	if r.LastFrame != nil {
		set("last_frame", "end_image_url", r.LastFrame.DataURL())
	}
	// Reference media. fal names these fields by endpoint family: the
	// reference-to-video family uses reference_image_urls / reference_video_urls
	// / reference_audio_urls, while other endpoints use the shorter names.
	// Sending the wrong one is silently ignored upstream, which looks like the
	// references were never applied. A model can override either name via
	// options.fields.
	imageURLs, videoURLs, audioURLs := "image_urls", "video_url", "audio_url"
	// The configured reference endpoint is reference-family by definition, even
	// when its path does not spell out "reference-to-video" (capability also
	// treats a "/reference" suffix as reference mode).
	refFamily := (o.UpstreamReference != "" && endpoint == o.UpstreamReference) ||
		strings.Contains(strings.ToLower(endpoint), "reference-to-video")
	if refFamily {
		imageURLs, videoURLs, audioURLs = "reference_image_urls", "reference_video_urls", "reference_audio_urls"
	}
	if len(r.References) > 0 {
		urls := make([]string, len(r.References))
		for i, img := range r.References {
			urls[i] = img.DataURL()
		}
		set("references", imageURLs, urls)
	}
	// The short names (video_url / audio_url) are scalar strings on fal; only
	// the reference_* family takes arrays.
	mediaURLs := func(key, name string, list []media.Input, array bool) {
		if len(list) == 0 {
			return
		}
		if !array {
			set(key, name, list[0].DataURL())
			return
		}
		urls := make([]string, len(list))
		for i, v := range list {
			urls[i] = v.DataURL()
		}
		set(key, name, urls)
	}
	mediaURLs("videos", videoURLs, r.Videos, refFamily)
	mediaURLs("audios", audioURLs, r.Audios, refFamily)
	return o.finish(in, r.Extra)
}

func buildSpeechInput(r media.SpeechRequest, c provider.Call, o modelOpts) map[string]any {
	in := map[string]any{}
	for k, v := range o.Defaults {
		in[k] = v
	}
	set := func(key, def string, v any) {
		if name, ok := o.field(key, def); ok {
			in[name] = v
		}
	}
	set("text", "text", r.Input)
	// A voice from the 音色库 is already a vendor id: skip alias remapping.
	voice := r.Voice
	if !r.VoiceResolved {
		voice = provider.ResolveVoice(c, voice)
	}
	if voice != "" {
		set("voice", "voice", voice)
	}
	if r.Speed > 0 {
		set("speed", "speed", r.Speed)
	}
	return o.finish(in, r.Extra)
}

// ---- adapter interfaces ----

func (a Adapter) GenerateImage(ctx context.Context, c provider.Call, r media.ImageRequest) (*media.ImageResult, error) {
	opts := parseOpts(c.Model.Options)
	endpoint := c.Model.Upstream
	if r.Edit && opts.UpstreamEdit != "" {
		endpoint = opts.UpstreamEdit
	}
	h, err := a.submit(ctx, c, endpoint, buildImageInput(r, opts))
	if err != nil {
		return nil, err
	}
	raw, err := a.pollResult(ctx, c, h)
	if err != nil {
		return nil, finalErr(h, err)
	}
	urls := imageURLs(decodeResult(raw))
	if len(urls) == 0 {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Final: true, Message: withRequestID("fal result has no image url", h.RequestID)}
	}
	res := &media.ImageResult{}
	for _, u := range urls {
		res.Images = append(res.Images, media.Asset{URL: u})
	}
	return res, nil
}

func (a Adapter) SubmitVideo(ctx context.Context, c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
	opts := parseOpts(c.Model.Options)
	endpoint := videoEndpoint(r, opts, c.Model.Upstream)
	h, err := a.submit(ctx, c, endpoint, buildVideoInput(r, opts, endpoint))
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(h)
	return media.TaskHandle(raw), nil
}

func (a Adapter) PollVideo(ctx context.Context, c provider.Call, raw media.TaskHandle) (media.TaskState, error) {
	var h handle
	if err := json.Unmarshal(raw, &h); err != nil {
		return media.TaskState{}, fmt.Errorf("%w: bad task handle", media.ErrBadRequest)
	}
	st, result, err := a.pollOnce(ctx, c, &h)
	if err != nil {
		return media.TaskState{}, err
	}
	switch st.Status {
	case "IN_QUEUE":
		return media.TaskState{Status: media.TaskQueued}, nil
	case "IN_PROGRESS":
		return media.TaskState{Status: media.TaskInProgress, Progress: 50}, nil
	case "COMPLETED":
		u := firstURLUnder(decodeResult(result), "video")
		if u == "" {
			return media.TaskState{Status: media.TaskFailed, Error: "fal result has no video url"}, nil
		}
		return media.TaskState{Status: media.TaskCompleted, Progress: 100, URL: u}, nil
	default:
		return media.TaskState{Status: media.TaskStatus(strings.ToLower(st.Status))}, nil
	}
}

func (a Adapter) Speak(ctx context.Context, c provider.Call, r media.SpeechRequest) (*media.Audio, error) {
	opts := parseOpts(c.Model.Options)
	h, err := a.submit(ctx, c, c.Model.Upstream, buildSpeechInput(r, c, opts))
	if err != nil {
		return nil, err
	}
	raw, err := a.pollResult(ctx, c, h)
	if err != nil {
		return nil, finalErr(h, err)
	}
	u := firstURLUnder(decodeResult(raw), "audio", "audio_file")
	if u == "" {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Final: true, Message: withRequestID("fal result has no audio url", h.RequestID)}
	}
	client := c.Stream
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := provider.FetchURL(ctx, client, u, "")
	if err != nil {
		return nil, finalErr(h, provider.TransportError(err, false))
	}
	if resp.StatusCode/100 != 2 {
		provider.ReadErrorBody(resp)
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode, Final: true, Message: withRequestID("fal audio download failed", h.RequestID)}
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = contentTypeFor(u, "audio/mpeg")
	}
	return &media.Audio{Body: resp.Body, ContentType: ct}, nil
}

func contentTypeFor(u, def string) string {
	p := u
	if parsed, err := url.Parse(u); err == nil && parsed.Path != "" {
		p = parsed.Path
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".opus", ".ogg":
		return "audio/ogg"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	}
	return def
}
