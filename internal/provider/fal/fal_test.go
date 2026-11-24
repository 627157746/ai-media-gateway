package fal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type fakeQueue struct {
	*httptest.Server
	mu           sync.Mutex
	submitPaths  []string
	submitBodies []map[string]any
	statusSeq    []string
	statusFixed  string
	statusHTTP   []int
	statusErr    string
	resultStatus int
	resultBody   any
	cancelPuts   int
	cancelAuths  []string
	fileBytes    []byte
}

func newFakeFal(t *testing.T) *fakeQueue {
	t.Helper()
	q := &fakeQueue{
		statusSeq:    []string{"IN_QUEUE", "IN_PROGRESS", "COMPLETED"},
		resultStatus: 200,
		fileBytes:    []byte("audio-bytes"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		if len(q.statusHTTP) > 0 {
			w.WriteHeader(q.statusHTTP[0])
			q.statusHTTP = q.statusHTTP[1:]
			return
		}
		st := "COMPLETED"
		if q.statusFixed != "" {
			st = q.statusFixed
		} else if len(q.statusSeq) > 0 {
			st = q.statusSeq[0]
			q.statusSeq = q.statusSeq[1:]
		}
		out := map[string]any{"status": st}
		if q.statusErr != "" {
			out["error"] = q.statusErr
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/result", func(w http.ResponseWriter, r *http.Request) {
		if q.resultStatus != 200 {
			w.WriteHeader(q.resultStatus)
		}
		_ = json.NewEncoder(w).Encode(q.resultBody)
	})
	mux.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		q.cancelPuts++
		q.cancelAuths = append(q.cancelAuths, r.Header.Get("Authorization"))
		q.mu.Unlock()
		w.WriteHeader(200)
	})
	mux.HandleFunc("/file.mp3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(q.fileBytes)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		q.mu.Lock()
		defer q.mu.Unlock()
		q.submitPaths = append(q.submitPaths, r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		q.submitBodies = append(q.submitBodies, body)
		base := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]string{
			"request_id":   "req1",
			"status_url":   base + "/status",
			"response_url": base + "/result",
			"cancel_url":   base + "/cancel",
		})
	})
	q.Server = httptest.NewServer(mux)
	t.Cleanup(q.Server.Close)
	return q
}

func (q *fakeQueue) lastBody() map[string]any {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.submitBodies[len(q.submitBodies)-1]
}

func call(srv *httptest.Server, upstream, opts string) provider.Call {
	return provider.Call{
		Provider: provider.ProviderConfig{BaseURL: srv.URL},
		Account:  provider.AccountConfig{APIKey: "key"},
		Model:    provider.ModelConfig{Upstream: upstream, Options: json.RawMessage(opts)},
		HTTP:     srv.Client(),
		Stream:   srv.Client(),
		Timeout:  10 * time.Second,
	}
}

func TestGenerateImageFlow(t *testing.T) {
	q := newFakeFal(t)
	q.resultBody = map[string]any{"images": []any{map[string]any{"url": "u1"}, map[string]any{"url": "u2"}}}
	res, err := Adapter{}.GenerateImage(context.Background(), call(q.Server, "m/up", ""), media.ImageRequest{Prompt: "p", N: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 2 || res.Images[0].URL != "u1" {
		t.Fatalf("%+v", res)
	}
	if q.lastBody()["num_images"] != float64(2) {
		t.Fatalf("body: %v", q.lastBody())
	}
}

func TestVideoSubmitAndPoll(t *testing.T) {
	q := newFakeFal(t)
	q.resultBody = map[string]any{"video": map[string]any{"url": "v1"}}
	ad := Adapter{}
	c := call(q.Server, "m/up", "")
	h, err := ad.SubmitVideo(context.Background(), c, media.VideoRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := ad.PollVideo(context.Background(), c, h)
	if err != nil || st.Status != media.TaskQueued {
		t.Fatalf("poll1: %+v %v", st, err)
	}
	st, _ = ad.PollVideo(context.Background(), c, h)
	if st.Status != media.TaskInProgress || st.Progress != 50 {
		t.Fatalf("poll2: %+v", st)
	}
	st, _ = ad.PollVideo(context.Background(), c, h)
	if st.Status != media.TaskCompleted || st.URL != "v1" || st.Progress != 100 {
		t.Fatalf("poll3: %+v", st)
	}
}

func TestCompletedWithErrorResult(t *testing.T) {
	q := newFakeFal(t)
	q.statusSeq = []string{"COMPLETED"}
	q.resultStatus = 422
	q.resultBody = map[string]any{"detail": "bad prompt"}
	ad := Adapter{}
	c := call(q.Server, "m/up", "")
	h, _ := ad.SubmitVideo(context.Background(), c, media.VideoRequest{Prompt: "p"})
	_, err := ad.PollVideo(context.Background(), c, h)
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || ue.Class != media.ClassInvalid {
		t.Fatalf("err: %v", err)
	}
}

func TestStatusErrorField(t *testing.T) {
	q := newFakeFal(t)
	q.statusSeq = []string{"COMPLETED"}
	q.statusErr = "prompt blocked"
	ad := Adapter{}
	c := call(q.Server, "m/up", "")
	h, _ := ad.SubmitVideo(context.Background(), c, media.VideoRequest{Prompt: "p"})
	_, err := ad.PollVideo(context.Background(), c, h)
	if err == nil || !strings.Contains(err.Error(), "prompt blocked") {
		t.Fatalf("err: %v", err)
	}
}

func TestVideoEndpointSelection(t *testing.T) {
	opts := `{"upstream_image":"a/i2v","upstream_reference":"a/ref"}`
	ad := Adapter{}
	ff := media.Input{Data: []byte("x"), MIME: "image/png"}
	q := newFakeFal(t)
	c := call(q.Server, "m/t2v", opts)
	ad.SubmitVideo(context.Background(), c, media.VideoRequest{Prompt: "p"})
	ad.SubmitVideo(context.Background(), c, media.VideoRequest{Prompt: "p", FirstFrame: &ff})
	ad.SubmitVideo(context.Background(), c, media.VideoRequest{Prompt: "p", Mode: "reference", References: []media.Input{ff}})
	want := []string{"/m/t2v", "/a/i2v", "/a/ref"}
	for i, p := range want {
		if q.submitPaths[i] != p {
			t.Fatalf("endpoint %d: %s want %s (all: %v)", i, q.submitPaths[i], p, q.submitPaths)
		}
	}
}

func TestImageEditEndpoint(t *testing.T) {
	q := newFakeFal(t)
	q.resultBody = map[string]any{"images": []any{map[string]any{"url": "u"}}}
	c := call(q.Server, "m/gen", `{"upstream_edit":"m/edit"}`)
	Adapter{}.GenerateImage(context.Background(), c, media.ImageRequest{Prompt: "p", Edit: true, Images: []media.Input{{Data: []byte("x")}}})
	if q.submitPaths[0] != "/m/edit" {
		t.Fatalf("paths: %v", q.submitPaths)
	}
	if _, ok := q.lastBody()["image_urls"]; !ok {
		t.Fatalf("no image_urls: %v", q.lastBody())
	}
}

func TestImageFieldMapping(t *testing.T) {
	q := newFakeFal(t)
	q.resultBody = map[string]any{"image": map[string]any{"url": "u"}}

	// size WxH -> image_size object; quality only via fields; defaults/drop
	opts := `{"fields":{"quality":"output_quality"}, "defaults":{"seed":42}, "drop":["seed"]}`
	c := call(q.Server, "m/up", opts)
	Adapter{}.GenerateImage(context.Background(), c, media.ImageRequest{
		Prompt: "p", N: 3, Size: "1024x768", Quality: "hd", Resolution: "high",
	})
	b := q.lastBody()
	if b["num_images"] != float64(3) {
		t.Fatalf("num_images: %v", b)
	}
	sz, _ := b["image_size"].(map[string]any)
	if sz == nil || sz["width"] != float64(1024) || sz["height"] != float64(768) {
		t.Fatalf("image_size: %v", b["image_size"])
	}
	if b["output_quality"] != "hd" || b["resolution"] != "high" {
		t.Fatalf("%v", b)
	}
	if _, ok := b["seed"]; ok {
		t.Fatal("drop not applied")
	}

	// aspect_ratio mode + "" field omission
	q.submitBodies = nil
	c = call(q.Server, "m/up", `{"fields":{"size_mode":"aspect_ratio","count":""}}`)
	Adapter{}.GenerateImage(context.Background(), c, media.ImageRequest{Prompt: "p", N: 2, Size: "1024x768"})
	b = q.lastBody()
	if b["aspect_ratio"] != "4:3" {
		t.Fatalf("aspect_ratio: %v", b["aspect_ratio"])
	}
	if _, ok := b["num_images"]; ok {
		t.Fatal(`"count" mapped to "" should be omitted`)
	}

	// a:b string under default mode -> aspect_ratio
	q.submitBodies = nil
	c = call(q.Server, "m/up", "")
	Adapter{}.GenerateImage(context.Background(), c, media.ImageRequest{Prompt: "p", Size: "16:9"})
	if q.lastBody()["aspect_ratio"] != "16:9" {
		t.Fatalf("%v", q.lastBody())
	}

	// extra merges last
	q.submitBodies = nil
	Adapter{}.GenerateImage(context.Background(), c, media.ImageRequest{Prompt: "p", Extra: map[string]any{"loras": []any{1}}})
	if _, ok := q.lastBody()["loras"]; !ok {
		t.Fatalf("extra not merged: %v", q.lastBody())
	}
}

func TestVideoFieldMapping(t *testing.T) {
	q := newFakeFal(t)
	q.resultBody = map[string]any{"video": map[string]any{"url": "v"}}
	ad := Adapter{}
	tr, fa := true, false
	ff, lf := media.Input{Data: []byte("a")}, media.Input{Data: []byte("b")}

	c := call(q.Server, "m/up", `{"fields":{"duration_format":"int"}}`)
	ad.SubmitVideo(context.Background(), c, media.VideoRequest{
		Prompt: "p", Seconds: "10", Size: "1280x720", Resolution: "720P",
		GenerateAudio: &tr, Watermark: &fa,
		FirstFrame: &ff, LastFrame: &lf,
		References: []media.Input{ff}, Videos: []media.Input{ff}, Audios: []media.Input{ff},
	})
	b := q.lastBody()
	checks := map[string]any{
		"prompt": "p", "duration": float64(10), "aspect_ratio": "16:9",
		"resolution": "720p", "generate_audio": true,
	}
	for k, want := range checks {
		if b[k] != want {
			t.Fatalf("%s: %v want %v (body %v)", k, b[k], want, b)
		}
	}
	for _, k := range []string{"image_url", "end_image_url", "image_urls", "video_url", "audio_url"} {
		if _, ok := b[k]; !ok {
			t.Fatalf("missing %s in %v", k, b)
		}
	}
	if _, ok := b["watermark"]; ok {
		t.Fatal("watermark should be dropped without fields.watermark")
	}

	// duration_format {n}s + custom field names + watermark field
	c = call(q.Server, "m/up", `{"fields":{"duration_format":"{n}s","duration":"dur","watermark":"wm"}}`)
	ad.SubmitVideo(context.Background(), c, media.VideoRequest{Prompt: "p", Seconds: "5", Watermark: &fa})
	b = q.lastBody()
	if b["dur"] != "5s" || b["wm"] != false {
		t.Fatalf("%v", b)
	}
	if _, ok := b["duration"]; ok {
		t.Fatal("custom duration field name not used")
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		class  media.ErrorClass
	}{
		{401, `{}`, media.ClassAuth},
		{402, `{}`, media.ClassQuota},
		{403, `{"detail":"account balance exhausted"}`, media.ClassQuota},
		{403, `{"detail":"forbidden"}`, media.ClassAuth},
		{422, `{"detail":[{"msg":"bad prompt","loc":["body","prompt"]}]}`, media.ClassInvalid},
		{429, `{}`, media.ClassRateLimit},
		{500, `{}`, media.ClassUpstream},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.status == 429 {
				w.Header().Set("Retry-After", "7")
			}
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		_, err := Adapter{}.SubmitVideo(context.Background(), call(srv, "m/up", ""), media.VideoRequest{Prompt: "p"})
		srv.Close()
		var ue *media.UpstreamError
		if !errors.As(err, &ue) || ue.Class != tc.class {
			t.Fatalf("%d: %v want class %v", tc.status, err, tc.class)
		}
		if tc.status == 422 && !strings.Contains(ue.Message, "bad prompt") {
			t.Fatalf("422 detail: %v", ue.Message)
		}
		if tc.status == 429 && ue.RetryAfter != 7*time.Second {
			t.Fatalf("retry-after: %v", ue.RetryAfter)
		}
	}
}

type errTransport struct{ err error }

func (e errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

func TestSubmitTransportFinal(t *testing.T) {
	// non-dial transport error on submit → Final (might have created a paid job)
	c := provider.Call{
		Provider: provider.ProviderConfig{BaseURL: "https://x"},
		Account:  provider.AccountConfig{APIKey: "k"},
		Model:    provider.ModelConfig{Upstream: "m"},
		HTTP:     &http.Client{Transport: errTransport{err: errors.New("write broke")}},
	}
	_, err := Adapter{}.SubmitVideo(context.Background(), c, media.VideoRequest{Prompt: "p"})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || !ue.Final {
		t.Fatalf("want Final: %v", err)
	}

	// dial error (connection refused) → not Final: nothing reached the vendor
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	c.HTTP = &http.Client{Timeout: 2 * time.Second}
	c.Provider.BaseURL = deadURL
	_, err = Adapter{}.SubmitVideo(context.Background(), c, media.VideoRequest{Prompt: "p"})
	if !errors.As(err, &ue) || ue.Final {
		t.Fatalf("dial error should not be Final: %v", err)
	}
}

func TestCancelOnCtxCancel(t *testing.T) {
	q := newFakeFal(t)
	q.statusSeq = []string{"IN_PROGRESS", "IN_PROGRESS", "IN_PROGRESS", "IN_PROGRESS", "IN_PROGRESS", "IN_PROGRESS"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := Adapter{}.GenerateImage(ctx, call(q.Server, "m/up", ""), media.ImageRequest{Prompt: "p"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		n := q.cancelPuts
		auths := q.cancelAuths
		q.mu.Unlock()
		if n > 0 {
			if auths[0] != "Key key" {
				t.Fatalf("cancel auth header: %q", auths[0])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cancel_url was not PUT")
}

func TestSpeak(t *testing.T) {
	q := newFakeFal(t)
	q.resultBody = map[string]any{"audio": map[string]any{"url": q.Server.URL + "/file.mp3"}}
	c := call(q.Server, "m/tts", "")
	c.Account.Options = json.RawMessage(`{"voices":{"narrator":"voice-42"}}`)
	a, err := Adapter{}.Speak(context.Background(), c, media.SpeechRequest{Input: "hello", Voice: "narrator", Speed: 1.1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Body.Close()
	b, _ := io.ReadAll(a.Body)
	if string(b) != "audio-bytes" || a.ContentType != "audio/mpeg" {
		t.Fatalf("audio: %q %q", b, a.ContentType)
	}
	if q.lastBody()["voice"] != "voice-42" || q.lastBody()["speed"] != 1.1 || q.lastBody()["text"] != "hello" {
		t.Fatalf("speech body: %v", q.lastBody())
	}
}

func fastPoll(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })
}

func TestPostSubmitErrorFinal(t *testing.T) {
	// submit succeeded, status carries an error → Final (no failover, no
	// duplicate paid job) and the message names the fal request.
	fastPoll(t)
	q := newFakeFal(t)
	q.statusSeq = []string{"COMPLETED"}
	q.statusErr = "prompt blocked"
	_, err := Adapter{}.GenerateImage(context.Background(), call(q.Server, "m/up", ""), media.ImageRequest{Prompt: "p"})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || !ue.Final || ue.Class != media.ClassInvalid {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(ue.Message, "prompt blocked") || !strings.Contains(ue.Message, "req1") {
		t.Fatalf("message: %q", ue.Message)
	}
	if len(q.submitPaths) != 1 {
		t.Fatalf("expected 1 submit, got %v", q.submitPaths)
	}
}

func TestTransientStatusErrorsRetried(t *testing.T) {
	fastPoll(t)
	// 500 + 429 + 500 poll failures, then COMPLETED → success
	q := newFakeFal(t)
	q.statusHTTP = []int{500, 429, 500}
	q.resultBody = map[string]any{"images": []any{map[string]any{"url": "u"}}}
	res, err := Adapter{}.GenerateImage(context.Background(), call(q.Server, "m/up", ""), media.ImageRequest{Prompt: "p"})
	if err != nil || len(res.Images) != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestTransientStatusErrorsGiveUp(t *testing.T) {
	fastPoll(t)
	// 5 consecutive transient poll errors → abort as Final upstream error
	q := newFakeFal(t)
	q.statusHTTP = []int{500, 500, 500, 500, 500}
	_, err := Adapter{}.GenerateImage(context.Background(), call(q.Server, "m/up", ""), media.ImageRequest{Prompt: "p"})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || !ue.Final {
		t.Fatalf("err: %v", err)
	}
	if len(q.submitPaths) != 1 {
		t.Fatalf("expected 1 submit, got %v", q.submitPaths)
	}
}

func TestPollOwnTimeout(t *testing.T) {
	fastPoll(t)
	q := newFakeFal(t)
	q.statusFixed = "IN_PROGRESS"
	c := call(q.Server, "m/up", "")
	c.Timeout = 20 * time.Millisecond
	_, err := Adapter{}.GenerateImage(context.Background(), c, media.ImageRequest{Prompt: "p"})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || !ue.Final || ue.Class != media.ClassUpstream {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(ue.Message, "timed out") || !strings.Contains(ue.Message, "req1") {
		t.Fatalf("message: %q", ue.Message)
	}
}
