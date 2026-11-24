package muse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

// pngBytes is a minimal PNG header so the result sniffer accepts it.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), []byte("rest-of-file")...)

// fakeMuse serves the job protocol and records the submitted body.
type fakeMuse struct {
	submitBody  map[string]any
	authHeader  string
	resultBytes []byte
	statuses    []string // polled statuses, consumed in order
	polls       int
	submits     int
	skipResult  bool
}

func (f *fakeMuse) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.authHeader = r.Header.Get("x-api-key")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/generate":
			f.submits++
			json.NewDecoder(r.Body).Decode(&f.submitBody)
			json.NewEncoder(w).Encode(map[string]any{"job_id": "job-1", "status": "queued"})
		case strings.HasSuffix(r.URL.Path, "/result"):
			w.Write(f.resultBytes)
		case strings.HasPrefix(r.URL.Path, "/jobs/"):
			st := "done"
			if f.polls < len(f.statuses) {
				st = f.statuses[f.polls]
			}
			f.polls++
			if st == "failed" {
				json.NewEncoder(w).Encode(map[string]any{"job_id": "job-1", "status": "failed", "error": "boom"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"job_id": "job-1", "status": st})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func call(srv *httptest.Server) provider.Call {
	return provider.Call{
		Provider: provider.ProviderConfig{BaseURL: srv.URL},
		Account:  provider.AccountConfig{APIKey: "muse-key"},
		HTTP:     srv.Client(),
	}
}

func withFastPoll(t *testing.T) {
	t.Helper()
	oldPoll, oldWait := pollInterval, longPollWait
	pollInterval = 0
	longPollWait = 0
	t.Cleanup(func() { pollInterval = oldPoll; longPollWait = oldWait })
}

// TestTextToImage covers the generate path: prompt plus optional format, and no
// image fields at all.
func TestTextToImage(t *testing.T) {
	withFastPoll(t)
	f := &fakeMuse{resultBytes: pngBytes}
	srv := f.server(t)

	res, err := Adapter{}.GenerateImage(context.Background(), call(srv), media.ImageRequest{
		Prompt:         "a red fox",
		N:              1,
		ResponseFormat: "webp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || res.Images[0].MIME != "image/png" {
		t.Fatalf("result: %+v", res.Images)
	}
	// The result is returned as base64 so the handler can answer b64_json.
	if _, err := base64.StdEncoding.DecodeString(res.Images[0].B64); err != nil {
		t.Fatalf("result not base64: %v", err)
	}
	if f.submitBody["prompt"] != "a red fox" {
		t.Fatalf("prompt: %v", f.submitBody["prompt"])
	}
	if f.submitBody["format"] != "webp" {
		t.Fatalf("format: %v", f.submitBody["format"])
	}
	if _, ok := f.submitBody["image_base64"]; ok {
		t.Fatalf("text-to-image must not send image_base64: %v", f.submitBody)
	}
	if f.authHeader != "muse-key" {
		t.Fatalf("x-api-key: %q", f.authHeader)
	}
}

// TestEditImageSingle covers image-to-image: the first image fixes composition
// via image_base64.
func TestEditImageSingle(t *testing.T) {
	withFastPoll(t)
	f := &fakeMuse{resultBytes: pngBytes}
	srv := f.server(t)

	src := []byte("source-bytes")
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv), media.ImageRequest{
		Prompt: "make it blue",
		N:      1,
		Edit:   true,
		Images: []media.Input{{Name: "src.png", MIME: "image/png", Data: src}},
	}); err != nil {
		t.Fatal(err)
	}

	got, _ := f.submitBody["image_base64"].(string)
	if got != base64.StdEncoding.EncodeToString(src) {
		t.Fatalf("image_base64 mismatch: %q", got)
	}
	if _, ok := f.submitBody["reference_images_base64"]; ok {
		t.Fatalf("a single image is the composition source, not a reference: %v", f.submitBody)
	}
}

// TestEditImageMultipleSplitsSourceAndRefs pins the split: image 1 fixes
// composition, the rest become pure references.
func TestEditImageMultipleSplitsSourceAndRefs(t *testing.T) {
	withFastPoll(t)
	f := &fakeMuse{resultBytes: pngBytes}
	srv := f.server(t)

	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv), media.ImageRequest{
		Prompt: "combine",
		N:      1,
		Edit:   true,
		Images: []media.Input{
			{Data: []byte("first")},
			{Data: []byte("second")},
			{Data: []byte("third")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if got, _ := f.submitBody["image_base64"].(string); got != base64.StdEncoding.EncodeToString([]byte("first")) {
		t.Fatalf("image_base64: %q", got)
	}
	refs, ok := f.submitBody["reference_images_base64"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("reference_images_base64 should hold the remaining 2: %v", f.submitBody["reference_images_base64"])
	}
	if refs[0] != base64.StdEncoding.EncodeToString([]byte("second")) {
		t.Fatalf("ref order: %v", refs)
	}
}

// TestDataURLImageAccepted makes sure an inline data URL is unwrapped rather
// than rejected.
func TestDataURLImageAccepted(t *testing.T) {
	withFastPoll(t)
	f := &fakeMuse{resultBytes: pngBytes}
	srv := f.server(t)

	payload := base64.StdEncoding.EncodeToString([]byte("inline"))
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv), media.ImageRequest{
		Prompt: "edit",
		N:      1,
		Edit:   true,
		Images: []media.Input{{URL: "data:image/png;base64," + payload}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.submitBody["image_base64"].(string); got != payload {
		t.Fatalf("image_base64: %q", got)
	}
}

// TestRemoteURLRejected documents a real limitation rather than silently
// sending a URL Muse cannot read: its input fields are base64 only.
func TestRemoteURLRejected(t *testing.T) {
	f := &fakeMuse{resultBytes: pngBytes}
	srv := f.server(t)

	_, err := (Adapter{}).GenerateImage(context.Background(), call(srv), media.ImageRequest{
		Prompt: "edit",
		N:      1,
		Edit:   true,
		Images: []media.Input{{URL: "https://example.com/a.png"}},
	})
	if err == nil {
		t.Fatal("a remote URL must be rejected")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Fatalf("error should explain the base64 requirement: %v", err)
	}
	if f.submits != 0 {
		t.Fatal("nothing should be submitted")
	}
}

// TestRejectsUnsupportedParameters guards the "do not send what the vendor does
// not accept" rule: Muse has no size/quality/background/resolution fields, so
// accepting them silently would mislead the client.
func TestRejectsUnsupportedParameters(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  media.ImageRequest
	}{
		{"size", media.ImageRequest{Prompt: "p", N: 1, Size: "1024x1024"}},
		{"quality", media.ImageRequest{Prompt: "p", N: 1, Quality: "high"}},
		{"background", media.ImageRequest{Prompt: "p", N: 1, Background: "opaque"}},
		{"resolution", media.ImageRequest{Prompt: "p", N: 1, Resolution: "2K"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMuse{resultBytes: pngBytes}
			srv := f.server(t)
			_, err := (Adapter{}).GenerateImage(context.Background(), call(srv), tc.req)
			if err == nil {
				t.Fatalf("%s should be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("error should name the parameter: %v", err)
			}
			if f.submits != 0 {
				t.Fatal("nothing should be submitted")
			}
		})
	}
	// auto size is the documented no-op and must be allowed.
	f := &fakeMuse{resultBytes: pngBytes}
	srv := f.server(t)
	withFastPoll(t)
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv),
		media.ImageRequest{Prompt: "p", N: 1, Size: "auto"}); err != nil {
		t.Fatalf("size=auto should be accepted: %v", err)
	}
}

// TestRejectsN over one and too many references.
func TestRejectsN(t *testing.T) {
	f := &fakeMuse{resultBytes: pngBytes}
	srv := f.server(t)
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv),
		media.ImageRequest{Prompt: "p", N: 2}); err == nil {
		t.Fatal("n>1 must be rejected")
	}

	// An edit takes image 1 as the source, so 1 source + 6 references is legal
	// (Muse caps only the references).
	imgs := make([]media.Input, 7)
	for i := range imgs {
		imgs[i] = media.Input{Data: []byte("x")}
	}
	withFastPoll(t)
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv),
		media.ImageRequest{Prompt: "p", N: 1, Edit: true, Images: imgs}); err != nil {
		t.Fatalf("1 source + 6 references must be accepted: %v", err)
	}
	// 1 source + 7 references exceeds the cap.
	tooMany := make([]media.Input, 8)
	for i := range tooMany {
		tooMany[i] = media.Input{Data: []byte("x")}
	}
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv),
		media.ImageRequest{Prompt: "p", N: 1, Edit: true, Images: tooMany}); err == nil {
		t.Fatal("1 source + 7 references must be rejected")
	}
	// Without Edit every image is a reference, so 7 alone exceeds the cap.
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv),
		media.ImageRequest{Prompt: "p", N: 1, Images: imgs}); err == nil {
		t.Fatal("7 reference images without a source must be rejected")
	}
}

// TestMaskRejected keeps an unsupported input from being ignored.
func TestMaskRejected(t *testing.T) {
	f := &fakeMuse{resultBytes: pngBytes}
	srv := f.server(t)
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv), media.ImageRequest{
		Prompt: "p", N: 1, Edit: true,
		Images: []media.Input{{Data: []byte("x")}},
		Mask:   &media.Input{Data: []byte("m")},
	}); err == nil {
		t.Fatal("mask must be rejected")
	}
}

// TestFailedJob surfaces the vendor's reason.
func TestFailedJob(t *testing.T) {
	withFastPoll(t)
	f := &fakeMuse{resultBytes: pngBytes, statuses: []string{"queued", "failed"}}
	srv := f.server(t)
	_, err := (Adapter{}).GenerateImage(context.Background(), call(srv), media.ImageRequest{Prompt: "p", N: 1})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected the vendor error, got %v", err)
	}
}

// TestPollsUntilDone makes sure an intermediate status does not abort.
func TestPollsUntilDone(t *testing.T) {
	withFastPoll(t)
	f := &fakeMuse{resultBytes: pngBytes, statuses: []string{"queued", "running", "running", "done"}}
	srv := f.server(t)
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv), media.ImageRequest{Prompt: "p", N: 1}); err != nil {
		t.Fatal(err)
	}
	if f.polls < 4 {
		t.Fatalf("should poll through intermediate states, polls=%d", f.polls)
	}
}

// TestResultSniffedAsWebP covers Muse returning WebP despite the requested
// format, which the adapter must report honestly.
func TestResultSniffedAsWebP(t *testing.T) {
	withFastPoll(t)
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), []byte("data")...)
	f := &fakeMuse{resultBytes: webp}
	srv := f.server(t)
	res, err := Adapter{}.GenerateImage(context.Background(), call(srv), media.ImageRequest{Prompt: "p", N: 1, ResponseFormat: "png"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Images[0].MIME != "image/webp" {
		t.Fatalf("MIME should be sniffed from bytes, got %q", res.Images[0].MIME)
	}
}

// TestNonImageResult rejected rather than returned as a broken image.
func TestNonImageResult(t *testing.T) {
	withFastPoll(t)
	f := &fakeMuse{resultBytes: []byte("not an image")}
	srv := f.server(t)
	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv), media.ImageRequest{Prompt: "p", N: 1}); err == nil {
		t.Fatal("a non-image result must error")
	}
}

// TestStatusErrorMapping pins the vendor-failure classes.
func TestStatusErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		code int
		want media.ErrorClass
	}{
		{401, media.ClassAuth},
		{402, media.ClassQuota},
		{429, media.ClassRateLimit},
		{400, media.ClassInvalid},
		{500, media.ClassUpstream},
	} {
		resp := &http.Response{
			StatusCode: tc.code,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"error":"nope"}`)),
		}
		ue, ok := statusError(resp).(*media.UpstreamError)
		if !ok {
			t.Fatalf("%d: not an UpstreamError", tc.code)
		}
		if ue.Class != tc.want {
			t.Fatalf("%d: class %v want %v", tc.code, ue.Class, tc.want)
		}
	}
}

// TestEmptyPrompt rejected.
func TestEmptyPrompt(t *testing.T) {
	if _, err := (Adapter{}).GenerateImage(context.Background(), provider.Call{}, media.ImageRequest{N: 1}); err == nil {
		t.Fatal("empty prompt must be rejected")
	}
}

// TestLongPollWait pins the ?wait=<seconds> query, which makes Muse hold the
// request open instead of the adapter sleeping between short polls.
func TestLongPollWait(t *testing.T) {
	oldPoll, oldWait := pollInterval, longPollWait
	pollInterval = 0
	longPollWait = 25 * time.Second
	t.Cleanup(func() { pollInterval = oldPoll; longPollWait = oldWait })

	var jobQuery string
	webp := pngBytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/generate":
			json.NewEncoder(w).Encode(map[string]any{"job_id": "j1", "status": "queued"})
		case strings.HasSuffix(r.URL.Path, "/result"):
			w.Write(webp)
		default:
			jobQuery = r.URL.RawQuery
			json.NewEncoder(w).Encode(map[string]any{"job_id": "j1", "status": "done"})
		}
	}))
	defer srv.Close()

	if _, err := (Adapter{}).GenerateImage(context.Background(), call(srv),
		media.ImageRequest{Prompt: "p", N: 1}); err != nil {
		t.Fatal(err)
	}
	if jobQuery != "wait=25" {
		t.Fatalf("job query should carry wait=25, got %q", jobQuery)
	}
}

// TestSourcePlusRefsSplit pins the three-way image split: with Edit the first
// image is the source and the rest are references; without Edit all are refs.
func TestSourcePlusRefsSplit(t *testing.T) {
	cases := []struct {
		name     string
		edit     bool
		images   []media.Input
		wantSrc  string
		wantRefs int
	}{
		{
			name:     "edit with source and refs",
			edit:     true,
			images:   []media.Input{{Data: []byte("src")}, {Data: []byte("r1")}, {Data: []byte("r2")}},
			wantSrc:  base64.StdEncoding.EncodeToString([]byte("src")),
			wantRefs: 2,
		},
		{
			name:     "text-to-image refs only",
			edit:     false,
			images:   []media.Input{{Data: []byte("r1")}, {Data: []byte("r2")}},
			wantSrc:  "",
			wantRefs: 2,
		},
		{
			name:     "edit with source only",
			edit:     true,
			images:   []media.Input{{Data: []byte("src")}},
			wantSrc:  base64.StdEncoding.EncodeToString([]byte("src")),
			wantRefs: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, err := buildInput(media.ImageRequest{Prompt: "p", N: 1, Edit: tc.edit, Images: tc.images})
			if err != nil {
				t.Fatal(err)
			}
			gotSrc, _ := in["image_base64"].(string)
			if gotSrc != tc.wantSrc {
				t.Fatalf("image_base64 = %q want %q", gotSrc, tc.wantSrc)
			}
			refs, _ := in["reference_images_base64"].([]string)
			if len(refs) != tc.wantRefs {
				t.Fatalf("refs = %d want %d", len(refs), tc.wantRefs)
			}
		})
	}
}

// TestDefaultFormatOmitted means Muse applies its own webp default rather than
// the adapter guessing a format the caller did not ask for.
func TestDefaultFormatOmitted(t *testing.T) {
	in, err := buildInput(media.ImageRequest{Prompt: "p", N: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := in["format"]; ok {
		t.Fatalf("format must be omitted when unset: %v", in)
	}
}
