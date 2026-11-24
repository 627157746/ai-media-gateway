package muse

// Muse video: one bridge endpoint (POST /video) covers text-to-video,
// image-to-video and reference-guided video. The vendor fixes the result at
// roughly 10 seconds of mp4, so the gateway must not pretend to offer duration,
// aspect ratio, resolution or format controls.
//
// Contract (per https://muse.627157.xyz, mirrored by the Pi video plugin):
//
//	POST /video                  {prompt, image_base64 | source_job,
//	                              reference_images_base64 | reference_jobs,
//	                              resume_from_job | resume_from_snapshot_id}
//	GET  /jobs/<id>              status: queued|running|done|failed
//	GET  /jobs/<id>/result       the mp4 bytes (requires x-api-key)
//
// Long polling (?wait=) must NOT be used here: Muse sits behind Cloudflare, which
// drops a connected response at about 60s. The job itself keeps running, so plain
// polling is the correct approach for video.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

// videoRefLimit is Muse's cap on reference images, shared with the image
// endpoint; reference_jobs and reference_images_base64 are counted together.
const videoRefLimit = 6

// videoPollInterval is the first wait between status polls. It grows up to
// videoPollMax because a video job takes roughly 10x longer than a still.
//
// var for tests.
var (
	videoPollInterval = 3 * time.Second
	videoPollMax      = 10 * time.Second
)

// videoHandle is what SubmitVideo stores; PollVideo needs no extra state but the
// job id, which keeps the handle readable in the task table.
type videoHandle struct {
	JobID string `json:"job_id"`
}

// SubmitVideo posts to /video and returns the job id.
func (a Adapter) SubmitVideo(ctx context.Context, c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
	if strings.TrimSpace(r.Prompt) == "" {
		return nil, &media.UpstreamError{Class: media.ClassInvalid, Message: "prompt is required", Final: true}
	}
	if err := rejectVideoControls(r); err != nil {
		return nil, err
	}

	body, err := buildVideoInput(r)
	if err != nil {
		return nil, err
	}

	u := c.BaseURL(defaultBase) + "/video"
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &media.UpstreamError{Class: media.ClassInvalid, Message: err.Error(), Final: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.Account.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, true)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	var j job
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode, Message: "bad video response: " + err.Error()}
	}
	if j.JobID == "" {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode, Message: "video response has no job_id"}
	}
	raw, _ := json.Marshal(videoHandle{JobID: j.JobID})
	return media.TaskHandle(raw), nil
}

// PollVideo reports the job's state. Completion points at the result endpoint;
// FetchVideo attaches the API key when the bytes are actually requested.
func (a Adapter) PollVideo(ctx context.Context, c provider.Call, h media.TaskHandle) (media.TaskState, error) {
	var vh videoHandle
	if err := json.Unmarshal(h, &vh); err != nil || vh.JobID == "" {
		return media.TaskState{}, &media.UpstreamError{Class: media.ClassInvalid, Message: "bad task handle", Final: true}
	}
	u := c.BaseURL(defaultBase) + "/jobs/" + vh.JobID
	j, err := a.jobOnce(ctx, c, u)
	if err != nil {
		return media.TaskState{}, err
	}
	switch j.Status {
	case "done":
		return media.TaskState{
			Status:   media.TaskCompleted,
			Progress: 100,
			URL:      c.BaseURL(defaultBase) + "/jobs/" + vh.JobID + "/result",
		}, nil
	case "failed":
		msg := j.Error
		if msg == "" {
			msg = "未知原因"
		}
		return media.TaskState{Status: media.TaskFailed, Error: "Muse 视频生成失败：" + msg}, nil
	case "running", "in_progress", "processing":
		return media.TaskState{Status: media.TaskInProgress, Progress: 50}, nil
	case "queued", "pending":
		return media.TaskState{Status: media.TaskQueued}, nil
	default:
		// Unknown state: keep polling rather than failing a live job.
		return media.TaskState{Status: media.TaskInProgress, Progress: 25}, nil
	}
}

// FetchVideo streams a finished video, adding the API key Muse requires. This is
// what makes the gateway's /content proxy work without leaking the key upstream.
func (a Adapter) FetchVideo(ctx context.Context, c provider.Call, resultURL, rangeHeader string) (*http.Response, error) {
	if resultURL == "" {
		return nil, &media.UpstreamError{Class: media.ClassInvalid, Message: "empty result url", Final: true}
	}
	client := c.Stream
	if client == nil {
		client = c.HTTP
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.Account.APIKey)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, false)
	}
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusPartialContent {
		defer resp.Body.Close()
		return nil, statusError(resp)
	}
	return resp, nil
}

// rejectVideoControls refuses parameters Muse does not have. Silently dropping
// them would let a client believe its duration or aspect ratio took effect when
// the vendor always produces the same ~10s clip.
func rejectVideoControls(r media.VideoRequest) error {
	if s := strings.TrimSpace(r.Seconds); s != "" {
		if n, err := strconv.Atoi(s); err != nil || n != 0 {
			return &media.UpstreamError{
				Class:   media.ClassInvalid,
				Final:   true,
				Message: "Muse 视频时长固定约 10 秒，不支持 seconds 参数",
			}
		}
	}
	if v := strings.TrimSpace(r.Ratio); v != "" && !strings.EqualFold(v, "auto") {
		return &media.UpstreamError{
			Class:   media.ClassInvalid,
			Final:   true,
			Message: "Muse 不支持 ratio：画幅请写进 prompt",
		}
	}
	if v := strings.TrimSpace(r.Size); v != "" && !strings.EqualFold(v, "auto") {
		return &media.UpstreamError{
			Class:   media.ClassInvalid,
			Final:   true,
			Message: "Muse 不支持 size：画幅请写进 prompt",
		}
	}
	if v := strings.TrimSpace(r.Resolution); v != "" {
		return &media.UpstreamError{
			Class:   media.ClassInvalid,
			Final:   true,
			Message: "Muse 不支持 resolution",
		}
	}
	if r.GenerateAudio != nil || r.Watermark != nil {
		return &media.UpstreamError{
			Class:   media.ClassInvalid,
			Final:   true,
			Message: "Muse 不支持 generate_audio / watermark",
		}
	}
	return nil
}

// buildVideoInput maps the request onto Muse's video body. Muse distinguishes a
// SOURCE image (which makes the job image-to-video) from REFERENCE images
// (identity/style hints), so the caller's mode decides how the uploads are read:
// a first frame in frames mode is the source, while images listed as references
// stay references.
func buildVideoInput(r media.VideoRequest) (map[string]any, error) {
	body := map[string]any{"prompt": r.Prompt}

	if r.FirstFrame != nil {
		b64, err := encodeInput(*r.FirstFrame)
		if err != nil {
			return nil, err
		}
		body["image_base64"] = b64
	}

	refs := r.References
	if len(refs) > 0 {
		if len(refs) > videoRefLimit {
			return nil, &media.UpstreamError{
				Class:   media.ClassInvalid,
				Final:   true,
				Message: "Muse 参考图最多 6 张",
			}
		}
		encoded := make([]string, 0, len(refs))
		for _, img := range refs {
			s, err := encodeInput(img)
			if err != nil {
				return nil, err
			}
			encoded = append(encoded, s)
		}
		body["reference_images_base64"] = encoded
	}
	return body, nil
}

var (
	_ provider.VideoGenerator = Adapter{}
	_ provider.VideoFetcher   = Adapter{}
)
