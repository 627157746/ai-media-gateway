package muse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

// fakeMuseVideo serves the video job protocol and records what was submitted.
type fakeMuseVideo struct {
	body      map[string]any
	authKey   string
	statuses  []string
	polls     int
	submitCnt int
	mp4       []byte
	resultKey string
	rangeSeen string
}

func (f *fakeMuseVideo) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.authKey = r.Header.Get("x-api-key")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/video":
			f.submitCnt++
			json.NewDecoder(r.Body).Decode(&f.body)
			json.NewEncoder(w).Encode(map[string]any{"job_id": "vj-1", "status": "queued"})
		case strings.HasSuffix(r.URL.Path, "/result"):
			f.resultKey = r.Header.Get("x-api-key")
			f.rangeSeen = r.Header.Get("Range")
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(f.mp4)
		case strings.HasPrefix(r.URL.Path, "/jobs/"):
			st := "done"
			if f.polls < len(f.statuses) {
				st = f.statuses[f.polls]
			}
			f.polls++
			if st == "failed" {
				json.NewEncoder(w).Encode(map[string]any{"job_id": "vj-1", "status": "failed", "error": "boom"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"job_id": "vj-1", "status": st, "snapshot_id": "snap-1",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func vcall(srv *httptest.Server) provider.Call {
	return provider.Call{
		Provider: provider.ProviderConfig{BaseURL: srv.URL},
		Account:  provider.AccountConfig{APIKey: "muse-key"},
		HTTP:     srv.Client(),
	}
}

// TestVideoSubmitTextToVideo covers the plain prompt path: no image fields.
func TestVideoSubmitTextToVideo(t *testing.T) {
	f := &fakeMuseVideo{mp4: []byte("mp4")}
	srv := f.server(t)

	h, err := (Adapter{}).SubmitVideo(context.Background(), vcall(srv), media.VideoRequest{
		Prompt: "a fox running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.body["prompt"] != "a fox running" {
		t.Fatalf("prompt: %v", f.body)
	}
	if _, ok := f.body["image_base64"]; ok {
		t.Fatalf("text-to-video must not send a source image: %v", f.body)
	}
	if f.authKey != "muse-key" {
		t.Fatalf("x-api-key: %q", f.authKey)
	}
	var vh videoHandle
	if err := json.Unmarshal(h, &vh); err != nil || vh.JobID != "vj-1" {
		t.Fatalf("handle: %s", string(h))
	}
}

// TestVideoSubmitFirstFrame covers image-to-video: the first frame becomes
// image_base64, which is what turns the job into an image-driven one.
func TestVideoSubmitFirstFrame(t *testing.T) {
	f := &fakeMuseVideo{mp4: []byte("mp4")}
	srv := f.server(t)

	src := []byte("frame-bytes")
	if _, err := (Adapter{}).SubmitVideo(context.Background(), vcall(srv), media.VideoRequest{
		Prompt:     "make it move",
		Mode:       "frames",
		FirstFrame: &media.Input{Name: "f.png", MIME: "image/png", Data: src},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.body["image_base64"].(string)
	if got != base64.StdEncoding.EncodeToString(src) {
		t.Fatalf("image_base64: %q", got)
	}
}

// TestVideoSubmitReferences covers reference mode and the 6-image cap.
func TestVideoSubmitReferences(t *testing.T) {
	f := &fakeMuseVideo{mp4: []byte("mp4")}
	srv := f.server(t)

	refs := make([]media.Input, 3)
	for i := range refs {
		refs[i] = media.Input{Data: []byte{byte(i)}}
	}
	if _, err := (Adapter{}).SubmitVideo(context.Background(), vcall(srv), media.VideoRequest{
		Prompt: "same character", Mode: "reference", References: refs,
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := f.body["reference_images_base64"].([]any)
	if !ok || len(got) != 3 {
		t.Fatalf("reference_images_base64: %v", f.body["reference_images_base64"])
	}

	// Seven references exceed Muse's cap and must be refused before submitting.
	tooMany := make([]media.Input, 7)
	for i := range tooMany {
		tooMany[i] = media.Input{Data: []byte{byte(i)}}
	}
	f2 := &fakeMuseVideo{mp4: []byte("mp4")}
	srv2 := f2.server(t)
	if _, err := (Adapter{}).SubmitVideo(context.Background(), vcall(srv2), media.VideoRequest{
		Prompt: "x", Mode: "reference", References: tooMany,
	}); err == nil {
		t.Fatal("7 references must be rejected")
	}
	if f2.submitCnt != 0 {
		t.Fatal("nothing should be submitted")
	}
}

// TestVideoRejectsUnsupportedControls is the core constraint: Muse fixes the
// output at ~10s mp4 with no aspect ratio, size, resolution or audio switches.
// Accepting them silently would let the client believe the request took effect.
// TestVideoIgnoresUnsupportedControls: controls Muse lacks are dropped, not
// rejected — the submitted body equals the one without them, and
// IgnoredVideoParams names exactly the fields that were set.
func TestVideoIgnoresUnsupportedControls(t *testing.T) {
	true_, false_ := true, false
	cases := []struct {
		name string
		req  media.VideoRequest
		want []string
	}{
		{"seconds", media.VideoRequest{Prompt: "p", Seconds: "10"}, []string{"seconds"}},
		{"ratio", media.VideoRequest{Prompt: "p", Ratio: "16:9"}, []string{"ratio"}},
		{"size", media.VideoRequest{Prompt: "p", Size: "1280x720"}, []string{"size"}},
		{"resolution", media.VideoRequest{Prompt: "p", Resolution: "1080p"}, []string{"resolution"}},
		{"generate_audio", media.VideoRequest{Prompt: "p", GenerateAudio: &true_}, []string{"generate_audio"}},
		{"watermark", media.VideoRequest{Prompt: "p", Watermark: &false_}, []string{"watermark"}},
		// Infinite Canvas OpenAI template defaults.
		{"canvas defaults", media.VideoRequest{
			Prompt: "p", Seconds: "8", Size: "1280x720", Resolution: "720p",
			GenerateAudio: &true_, Watermark: &false_,
		}, []string{"seconds", "size", "resolution", "generate_audio", "watermark"}},
		// Empty / "auto" / 0 are no-ops and are not reported.
		{"no-ops", media.VideoRequest{Prompt: "p", Seconds: "0", Ratio: "auto", Size: "AUTO"}, nil},
	}

	base := &fakeMuseVideo{mp4: []byte("mp4")}
	if _, err := (Adapter{}).SubmitVideo(context.Background(), vcall(base.server(t)), media.VideoRequest{Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	wantBody, _ := json.Marshal(base.body)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Adapter{}).IgnoredVideoParams(tc.req); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("ignored = %v, want %v", got, tc.want)
			}
			f := &fakeMuseVideo{mp4: []byte("mp4")}
			if _, err := (Adapter{}).SubmitVideo(context.Background(), vcall(f.server(t)), tc.req); err != nil {
				t.Fatalf("%s should be accepted: %v", tc.name, err)
			}
			if f.submitCnt != 1 {
				t.Fatalf("submits = %d", f.submitCnt)
			}
			if gotBody, _ := json.Marshal(f.body); string(gotBody) != string(wantBody) {
				t.Fatalf("body leaked ignored controls: %s, want %s", gotBody, wantBody)
			}
		})
	}
}

// TestVideoPollStates maps the job status onto the gateway's task states.
func TestVideoPollStates(t *testing.T) {
	f := &fakeMuseVideo{mp4: []byte("mp4"), statuses: []string{"queued", "running", "done"}}
	srv := f.server(t)
	a := Adapter{}
	c := vcall(srv)
	raw, _ := json.Marshal(videoHandle{JobID: "vj-1"})

	want := []media.TaskStatus{media.TaskQueued, media.TaskInProgress, media.TaskCompleted}
	for i, w := range want {
		st, err := a.PollVideo(context.Background(), c, media.TaskHandle(raw))
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if st.Status != w {
			t.Fatalf("poll %d: got %v want %v", i, st.Status, w)
		}
		if w == media.TaskCompleted && !strings.HasSuffix(st.URL, "/jobs/vj-1/result") {
			t.Fatalf("completed URL should point at the result: %q", st.URL)
		}
	}
}

// TestVideoPollFailed surfaces the vendor's reason.
func TestVideoPollFailed(t *testing.T) {
	f := &fakeMuseVideo{mp4: []byte("mp4"), statuses: []string{"failed"}}
	srv := f.server(t)
	raw, _ := json.Marshal(videoHandle{JobID: "vj-1"})
	st, err := Adapter{}.PollVideo(context.Background(), vcall(srv), media.TaskHandle(raw))
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != media.TaskFailed || !strings.Contains(st.Error, "boom") {
		t.Fatalf("state: %+v", st)
	}
}

// TestVideoFetchSendsKey is the reason VideoFetcher exists: Muse gates the
// result behind x-api-key, which the gateway's plain GET would never send.
func TestVideoFetchSendsKey(t *testing.T) {
	f := &fakeMuseVideo{mp4: []byte("mp4-bytes")}
	srv := f.server(t)

	url := srv.URL + "/jobs/vj-1/result"
	resp, err := Adapter{}.FetchVideo(context.Background(), vcall(srv), url, "bytes=0-99")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if f.resultKey != "muse-key" {
		t.Fatalf("result must carry x-api-key, got %q", f.resultKey)
	}
	if f.rangeSeen != "bytes=0-99" {
		t.Fatalf("Range should be forwarded, got %q", f.rangeSeen)
	}
}

// TestVideoFetchNoLongPoll pins the operational note: Muse sits behind
// Cloudflare, which drops a held connection near 60s, so video polling must not
// append ?wait=.
func TestVideoFetchNoLongPoll(t *testing.T) {
	var sawWait bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "wait") {
			sawWait = true
		}
		json.NewEncoder(w).Encode(map[string]any{"job_id": "vj-1", "status": "running"})
	}))
	defer srv.Close()

	raw, _ := json.Marshal(videoHandle{JobID: "vj-1"})
	if _, err := (Adapter{}).PollVideo(context.Background(), vcall(srv), media.TaskHandle(raw)); err != nil {
		t.Fatal(err)
	}
	if sawWait {
		t.Fatal("video polling must not use ?wait= (Cloudflare drops it at ~60s)")
	}
}

// TestVideoEmptyPrompt rejected before any network call.
func TestVideoEmptyPrompt(t *testing.T) {
	if _, err := (Adapter{}).SubmitVideo(context.Background(), provider.Call{}, media.VideoRequest{}); err == nil {
		t.Fatal("empty prompt must be rejected")
	}
}

func TestVideoEmptyResultURL(t *testing.T) {
	if _, err := (Adapter{}).FetchVideo(context.Background(), provider.Call{}, "", ""); err == nil {
		t.Fatal("empty result url must be rejected")
	}
}
