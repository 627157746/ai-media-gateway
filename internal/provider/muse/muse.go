// Package muse implements the Muse media bridge adapter (image generation and
// editing). Muse is job-based: a submit returns a job id, which is polled until
// terminal, then the bytes are fetched from the result endpoint.
//
// One endpoint serves both text-to-image and image-to-image, so the request
// shape decides the mode rather than a separate URL:
//
//	text-to-image      {prompt}
//	image-to-image     {prompt, image_base64}            (first image = composition)
//	reference-only     {prompt, reference_images_base64} (style/character refs)
package muse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type Adapter struct{}

func (Adapter) Type() string { return "muse" }

func init() { provider.Register("muse", Adapter{}) }

const defaultBase = "https://muse.627157.xyz"

// maxReferenceImages is Muse's documented cap on reference images.
const maxReferenceImages = 6

// var for tests.
var pollInterval = 2 * time.Second

// ---- job protocol ----

type job struct {
	JobID      string `json:"job_id"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	SnapshotID string `json:"snapshot_id"`
	ResultURL  string `json:"result_url"`
}

// GenerateImage submits a job, waits for it, and downloads the produced bytes.
func (a Adapter) GenerateImage(ctx context.Context, c provider.Call, r media.ImageRequest) (*media.ImageResult, error) {
	if strings.TrimSpace(r.Prompt) == "" {
		return nil, &media.UpstreamError{Class: media.ClassInvalid, Message: "prompt is required", Final: true}
	}
	// Muse returns exactly one image per job (verified against the Pi plugin:
	// n>1 is rejected rather than fanned out into multiple jobs).
	if r.N > 1 {
		return nil, &media.UpstreamError{
			Class:   media.ClassInvalid,
			Message: "Muse 一次只产出一张图，不支持 n>1",
			Final:   true,
		}
	}
	// The 6-image cap applies to reference images only. A source image
	// (image_base64 / source_job), which turns the job into an edit, is counted
	// separately by Muse, so "1 source + 6 references" is a valid request.
	if n := len(referenceImages(r)); n > maxReferenceImages {
		return nil, &media.UpstreamError{
			Class:   media.ClassInvalid,
			Message: "Muse 最多支持 6 张参考图",
			Final:   true,
		}
	}
	// Muse does not accept arbitrary pixel sizes or quality tiers. Silently
	// dropping them would let a client believe they took effect.
	for field, v := range map[string]string{
		"size":       r.Size,
		"quality":    r.Quality,
		"background": r.Background,
		"resolution": r.Resolution,
	} {
		if strings.TrimSpace(v) != "" && !(field == "size" && strings.EqualFold(v, "auto")) {
			return nil, &media.UpstreamError{
				Class:   media.ClassInvalid,
				Message: "Muse 不支持 " + field + " 参数，请从请求中移除",
				Final:   true,
			}
		}
	}
	if r.Mask != nil {
		return nil, &media.UpstreamError{Class: media.ClassInvalid, Message: "Muse 不支持蒙版（mask）", Final: true}
	}

	body, err := buildInput(r)
	if err != nil {
		return nil, err
	}
	j, err := a.submit(ctx, c, body)
	if err != nil {
		return nil, err
	}
	done, err := a.await(ctx, c, j.JobID)
	if err != nil {
		return nil, err
	}
	return a.fetchResult(ctx, c, done.JobID)
}

// sourceImage returns the image that turns a job into an edit, or nil for pure
// text-to-image. Muse has no separate edit endpoint on /generate: supplying a
// source image is what makes the job an edit.
//
// When the caller marked the request as an edit, the first image is the source
// the prompt operates on. Otherwise every image is a pure reference.
func sourceImage(r media.ImageRequest) *media.Input {
	if !r.Edit || len(r.Images) == 0 {
		return nil
	}
	img := r.Images[0]
	return &img
}

// referenceImages returns the images that are pure references (style, character,
// scene). Muse merges these with reference_jobs and caps the total at 6.
func referenceImages(r media.ImageRequest) []media.Input {
	if r.Edit && len(r.Images) > 0 {
		return r.Images[1:]
	}
	return r.Images
}

// buildInput maps the gateway request onto Muse's input shape.
func buildInput(r media.ImageRequest) (map[string]any, error) {
	in := map[string]any{"prompt": r.Prompt}
	// response_format carries the desired file type when it names one; the
	// url/b64_json markers are response shapes, not formats.
	if f := strings.TrimSpace(r.ResponseFormat); f != "" && f != "url" && f != "b64_json" {
		in["format"] = f
	}

	if src := sourceImage(r); src != nil {
		b64, err := encodeInput(*src)
		if err != nil {
			return nil, err
		}
		in["image_base64"] = b64
	}

	if refs := referenceImages(r); len(refs) > 0 {
		encoded := make([]string, 0, len(refs))
		for _, img := range refs {
			s, err := encodeInput(img)
			if err != nil {
				return nil, err
			}
			encoded = append(encoded, s)
		}
		in["reference_images_base64"] = encoded
	}
	return in, nil
}

// encodeInput turns a media.Input into a bare base64 string. Muse expects the
// payload without a data: prefix.
func encodeInput(in media.Input) (string, error) {
	if len(in.Data) > 0 {
		return base64.StdEncoding.EncodeToString(in.Data), nil
	}
	if in.URL == "" {
		return "", &media.UpstreamError{Class: media.ClassInvalid, Message: "参考图既没有数据也没有 URL", Final: true}
	}
	u := strings.TrimSpace(in.URL)
	if i := strings.Index(u, "base64,"); i >= 0 {
		return u[i+len("base64,"):], nil
	}
	if strings.HasPrefix(u, "data:") {
		// A data URL that is not base64-encoded cannot be sent as-is.
		return "", &media.UpstreamError{Class: media.ClassInvalid, Message: "参考图 data URL 不是 base64 编码", Final: true}
	}
	return "", &media.UpstreamError{
		Class:   media.ClassInvalid,
		Final:   true,
		Message: "参考图必须先下载再上传：Muse 只接受 base64 图像数据" + "（收到 URL " + truncate(u, 80) + "）",
	}
}

func (a Adapter) submit(ctx context.Context, c provider.Call, input map[string]any) (*job, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, &media.UpstreamError{Class: media.ClassInvalid, Message: err.Error(), Final: true}
	}
	u := c.BaseURL(defaultBase) + "/generate"
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
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode, Message: "bad generate response: " + err.Error()}
	}
	if j.JobID == "" {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode, Message: "generate response has no job_id"}
	}
	return &j, nil
}

// await polls the job until a terminal state. Muse reports done/failed; an
// unknown status keeps polling so a new intermediate state does not abort the
// request, and the overall budget bounds the wait.
// longPollWait is the value sent as ?wait= on the job query. Muse holds the
// request open until the job finishes or this many seconds elapse, which turns
// polling into a single blocking call instead of a series of short ones.
// var for tests.
var longPollWait = 25 * time.Second

func (a Adapter) await(ctx context.Context, c provider.Call, jobID string) (*job, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 4 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u := c.BaseURL(defaultBase) + "/jobs/" + jobID
	if longPollWait > 0 {
		u += "?wait=" + strconv.Itoa(int(longPollWait/time.Second))
	}
	for {
		j, err := a.jobOnce(ctx, c, u)
		if err != nil {
			// A cancelled/expired budget is reported as a timeout, not a
			// vendor failure.
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return nil, &media.UpstreamError{
					Class:   media.ClassUpstream,
					Message: "Muse 任务超时：" + jobID,
					Final:   true,
				}
			}
			return nil, err
		}
		switch j.Status {
		case "done":
			return j, nil
		case "failed":
			msg := j.Error
			if msg == "" {
				msg = "未知原因"
			}
			return nil, &media.UpstreamError{Class: media.ClassUpstream, Message: "Muse 任务失败：" + msg, Final: true}
		}
		// The long poll returned before completion (its own wait expired), so
		// issue another call. Only fall back to a visible sleep when long
		// polling is disabled.
		if longPollWait > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return nil, &media.UpstreamError{Class: media.ClassUpstream, Message: "Muse 任务超时：" + jobID, Final: true}
		case <-time.After(pollInterval):
		}
	}
}

func (a Adapter) jobOnce(ctx context.Context, c provider.Call, u string) (*job, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.Account.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, false)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	var j job
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Status: resp.StatusCode, Message: "bad job response"}
	}
	return &j, nil
}

// fetchResult downloads the produced image and sniffs its real type, because
// Muse's /result may answer WebP regardless of the requested format.
//
// The result endpoint is behind the same x-api-key auth as the rest of the API,
// so this builds its own request instead of using provider.FetchURL (which is
// for unauthenticated vendor CDN links).
func (a Adapter) fetchResult(ctx context.Context, c provider.Call, jobID string) (*media.ImageResult, error) {
	u := c.BaseURL(defaultBase) + "/jobs/" + jobID + "/result"
	client := c.Stream
	if client == nil {
		client = c.HTTP
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.Account.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, false)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Message: "读取结果失败：" + err.Error()}
	}
	mime := sniffImage(data)
	if mime == "" {
		return nil, &media.UpstreamError{Class: media.ClassUpstream, Final: true, Message: "Muse 结果不是可识别的图片"}
	}
	return &media.ImageResult{Images: []media.Asset{{B64: base64.StdEncoding.EncodeToString(data), MIME: mime}}}, nil
}

// sniffImage identifies the image type from magic bytes.
func sniffImage(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return "image/jpeg"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	case len(b) >= 6 && bytes.Equal(b[:6], []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 6 && bytes.Equal(b[:6], []byte("GIF87a")):
		return "image/gif"
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// statusError maps a Muse HTTP failure onto the gateway's error classes. Muse
// authenticates with x-api-key, so 401/403 are auth failures, 402 is quota.
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
		for _, w := range []string{"balance", "exhausted", "quota", "credit", "billing"} {
			if strings.Contains(lower, w) {
				ue.Class = media.ClassQuota
				break
			}
		}
	case http.StatusTooManyRequests:
		ue.Class = media.ClassRateLimit
		ue.RetryAfter = provider.ParseRetryAfter(resp.Header)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		ue.Class = media.ClassInvalid
		ue.Final = true
	default:
		ue.Class = media.ClassUpstream
	}
	return ue
}

func detailMessage(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		for _, k := range []string{"error", "message", "detail"} {
			if v, ok := m[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
				if o, ok := v.(map[string]any); ok {
					if s, ok := o["message"].(string); ok && s != "" {
						return s
					}
				}
			}
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
