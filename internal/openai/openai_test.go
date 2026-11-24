package openai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/admin"
	"ai-media-gateway/internal/config"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/openai"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/secret"
	"ai-media-gateway/internal/server"
	"ai-media-gateway/internal/store"
)

// fakeAd implements all three generator interfaces under one type.
type fakeAd struct {
	videoReqs []media.VideoRequest
	pollCalls []string
	submitFn  func(c provider.Call, r media.VideoRequest) (media.TaskHandle, error)
	pollFn    func(c provider.Call, h media.TaskHandle) (media.TaskState, error)
	imgFn     func(c provider.Call, r media.ImageRequest) (*media.ImageResult, error)
	speakFn   func(c provider.Call, r media.SpeechRequest) (*media.Audio, error)
}

var fake = &fakeAd{}

func (fakeAd) Type() string { return "fakeall" }

func (a *fakeAd) SubmitVideo(_ context.Context, c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
	a.videoReqs = append(a.videoReqs, r)
	return a.submitFn(c, r)
}
func (a *fakeAd) PollVideo(_ context.Context, c provider.Call, h media.TaskHandle) (media.TaskState, error) {
	a.pollCalls = append(a.pollCalls, c.Account.ID)
	return a.pollFn(c, h)
}
func (a *fakeAd) GenerateImage(_ context.Context, c provider.Call, r media.ImageRequest) (*media.ImageResult, error) {
	return a.imgFn(c, r)
}
func (a *fakeAd) Speak(_ context.Context, c provider.Call, r media.SpeechRequest) (*media.Audio, error) {
	return a.speakFn(c, r)
}

// imageOnlyAd lacks VideoGenerator → unsupported kind check.
type imageOnlyAd struct{}

func (imageOnlyAd) Type() string { return "fakeimg" }
func (imageOnlyAd) GenerateImage(_ context.Context, c provider.Call, r media.ImageRequest) (*media.ImageResult, error) {
	return &media.ImageResult{}, nil
}

func init() {
	provider.Register("fakeall", fake)
	provider.Register("fakeimg", imageOnlyAd{})
}

type env struct {
	t   *testing.T
	st  *store.Store
	srv http.Handler
	key string
	box *secret.Box
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box := secret.New("mk")
	pool := accounts.New(st, box, config.Cooldowns{
		RateLimit: time.Minute, Upstream: 30 * time.Second, UpstreamMax: 10 * time.Minute,
		Quota: time.Hour, Auth: 24 * time.Hour,
	}, http.DefaultClient, nil)
	cfg := config.Config{AdminToken: "adm", MaxBodyBytes: 64 << 20, CORSOrigins: []string{"*"}}
	_, key, _ := st.CreateAPIKey(context.Background(), "t")
	return &env{
		t: t, st: st, box: box, key: key,
		srv: server.New(server.Deps{
			Config: cfg, Store: st, Pool: pool,
			Admin: admin.New(st, box), OpenAI: openai.New(st, pool),
		}),
	}
}

func (e *env) account(t *testing.T, id, provID, key string) {
	t.Helper()
	enc, _ := e.box.Seal([]byte(key))
	if err := e.st.CreateAccount(context.Background(), &store.Account{
		ID: id, ProviderID: provID, APIKeyEnc: enc, Enabled: true, Weight: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) do(req *http.Request) *httptest.ResponseRecorder {
	req.Header.Set("Authorization", "Bearer "+e.key)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func (e *env) jsonReq(method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return e.do(req)
}

func seedVideo(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	e.st.CreateProvider(ctx, &store.Provider{ID: "p1", Type: "fakeall", Enabled: true})
	e.account(t, "acc-a", "p1", "key-A")
	e.account(t, "acc-b", "p1", "key-B")
	e.st.CreateModel(ctx, &store.Model{ID: "vid", ProviderID: "p1", Kind: media.KindVideo, Upstream: "up/vid", Enabled: true})
	e.st.CreateModel(ctx, &store.Model{ID: "img", ProviderID: "p1", Kind: media.KindImage, Upstream: "up/img", Enabled: true})
	e.st.CreateModel(ctx, &store.Model{ID: "tts", ProviderID: "p1", Kind: media.KindSpeech, Upstream: "up/tts", Enabled: true})
}

func multipartReq(t *testing.T, path string, fields map[string]string, files map[string][][]byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for name, parts := range files {
		for i, data := range parts {
			hdr := textproto.MIMEHeader{}
			hdr.Set("Content-Disposition", `form-data; name="`+name+`"; filename="f`+strings.Repeat("i", i+1)+`.bin"`)
			hdr.Set("Content-Type", "application/octet-stream")
			pw, _ := mw.CreatePart(hdr)
			pw.Write(data)
		}
	}
	mw.Close()
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestVideoMultipartCanvasFields(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.videoReqs = nil
	fake.submitFn = func(c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
		return media.TaskHandle(`{"h":"1"}`), nil
	}
	req := multipartReq(t, "/v1/videos", map[string]string{
		"model": "vid", "prompt": "a cat", "seconds": "10", "size": "1280x720",
		"resolution_name": "720p", "generate_audio": "true", "watermark": "false",
		"mode": "frames",
	}, map[string][][]byte{
		"first_frame": {[]byte("FF")},
		"last_frame":  {[]byte("LF")},
		"image[]":     {[]byte("R1"), []byte("R2")},
		"video[]":     {[]byte("VD")},
		"audio[]":     {[]byte("AU")},
	})
	rec := e.do(req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["object"] != "video" || out["status"] != "queued" || !strings.HasPrefix(out["id"].(string), "video_") {
		t.Fatalf("resp: %v", out)
	}
	r := fake.videoReqs[0]
	if r.Prompt != "a cat" || r.Seconds != "10" || r.Size != "1280x720" || r.Resolution != "720p" ||
		r.Mode != "frames" || r.GenerateAudio == nil || !*r.GenerateAudio || r.Watermark == nil || *r.Watermark {
		t.Fatalf("request: %+v", r)
	}
	if r.FirstFrame == nil || string(r.FirstFrame.Data) != "FF" || r.LastFrame == nil || string(r.LastFrame.Data) != "LF" {
		t.Fatal("frames missing")
	}
	if len(r.References) != 2 || len(r.Videos) != 1 || len(r.Audios) != 1 {
		t.Fatalf("refs=%d vids=%d auds=%d", len(r.References), len(r.Videos), len(r.Audios))
	}
	// task bound to the account pool used
	task, err := e.st.GetVideoTask(context.Background(), out["id"].(string))
	if err != nil || task.AccountID == "" || task.ProviderID != "p1" || task.ModelID != "vid" {
		t.Fatalf("task: %+v %v", task, err)
	}
	// request log captured model/provider/account/attempts
	logs, _ := e.st.ListRequestLogs(context.Background(), "", time.Now().Add(-time.Minute), 10)
	if len(logs) == 0 || logs[0].ModelID != "vid" || logs[0].ProviderID != "p1" || logs[0].AccountID == "" || logs[0].Attempts != 1 {
		t.Fatalf("log: %+v", logs)
	}
}

func TestVideoModeNormalization(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.videoReqs = nil
	fake.submitFn = func(c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
		return media.TaskHandle(`{}`), nil
	}
	mk := func(s string) string { return "data:image/png;base64," + s }
	cases := []struct {
		body     map[string]any
		wantMode string
		check    func(r media.VideoRequest) bool
	}{
		{map[string]any{"model": "vid", "prompt": "p", "images": []any{mk("QQ=="), mk("Qg==")}},
			"frames", func(r media.VideoRequest) bool {
				return r.FirstFrame != nil && r.LastFrame != nil && len(r.References) == 0
			}},
		{map[string]any{"model": "vid", "prompt": "p", "images": []any{mk("QQ=="), mk("Qg=="), mk("Qw==")}},
			"reference", func(r media.VideoRequest) bool { return len(r.References) == 3 }},
		{map[string]any{"model": "vid", "prompt": "p", "first_frame": mk("QQ==")},
			"frames", func(r media.VideoRequest) bool { return r.FirstFrame != nil }},
		{map[string]any{"model": "vid", "prompt": "p", "mode": "reference", "images": []any{mk("QQ==")}},
			"reference", func(r media.VideoRequest) bool { return len(r.References) == 1 }},
		{map[string]any{"model": "vid", "prompt": "p", "input_reference": mk("QQ==")},
			"frames", func(r media.VideoRequest) bool { return r.FirstFrame != nil }},
	}
	for i, tc := range cases {
		fake.videoReqs = nil
		rec := e.jsonReq("POST", "/v1/videos", tc.body)
		if rec.Code != 200 {
			t.Fatalf("case %d: %d %s", i, rec.Code, rec.Body)
		}
		r := fake.videoReqs[0]
		if r.Mode != tc.wantMode || !tc.check(r) {
			t.Fatalf("case %d: mode=%s want %s req=%+v", i, r.Mode, tc.wantMode, r)
		}
	}
}

func TestVideoFailoverAndAffinity(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.submitFn = func(c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
		if c.Account.APIKey == "key-A" {
			return nil, &media.UpstreamError{Class: media.ClassAuth, Status: 401, Message: "bad key"}
		}
		return media.TaskHandle(`{"h":"B"}`), nil
	}
	polled := []string{}
	fake.pollCalls = nil
	fake.pollFn = func(c provider.Call, h media.TaskHandle) (media.TaskState, error) {
		polled = append(polled, c.Account.ID)
		return media.TaskState{Status: media.TaskCompleted, Progress: 100, URL: "http://cdn/v.mp4"}, nil
	}
	rec := e.jsonReq("POST", "/v1/videos", map[string]any{"model": "vid", "prompt": "p"})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var task map[string]any
	json.Unmarshal(rec.Body.Bytes(), &task)
	id := task["id"].(string)
	row, _ := e.st.GetVideoTask(context.Background(), id)
	if row.AccountID != "acc-b" {
		t.Fatalf("task bound to %s", row.AccountID)
	}
	logs, _ := e.st.ListRequestLogs(context.Background(), "", time.Time{}, 10)
	if logs[0].Attempts != 2 {
		t.Fatalf("attempts: %+v", logs[0])
	}
	// poll → completed, video_url, and only acc-b polled
	rec = e.do(httptest.NewRequest("GET", "/v1/videos/"+id, nil))
	var obj map[string]any
	json.Unmarshal(rec.Body.Bytes(), &obj)
	if obj["status"] != "completed" || obj["video_url"] != "http://cdn/v.mp4" {
		t.Fatalf("poll obj: %v", obj)
	}
	for _, a := range polled {
		if a != "acc-b" {
			t.Fatalf("poll hit wrong account: %v", polled)
		}
	}
}

func TestVideoPollTransientKeepsState(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.submitFn = func(c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
		return media.TaskHandle(`{}`), nil
	}
	fake.pollFn = func(c provider.Call, h media.TaskHandle) (media.TaskState, error) {
		return media.TaskState{}, &media.UpstreamError{Class: media.ClassUpstream, Status: 502, Message: "flaky"}
	}
	rec := e.jsonReq("POST", "/v1/videos", map[string]any{"model": "vid", "prompt": "p"})
	var task map[string]any
	json.Unmarshal(rec.Body.Bytes(), &task)
	time.Sleep(1100 * time.Millisecond) // past the 1s poll throttle
	rec = e.do(httptest.NewRequest("GET", "/v1/videos/"+task["id"].(string), nil))
	var obj map[string]any
	json.Unmarshal(rec.Body.Bytes(), &obj)
	if obj["status"] == "failed" {
		t.Fatalf("transient error failed the task: %v", obj)
	}
}

func TestVideoDeletedAccountFails(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.submitFn = func(c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
		return media.TaskHandle(`{}`), nil
	}
	rec := e.jsonReq("POST", "/v1/videos", map[string]any{"model": "vid", "prompt": "p"})
	var task map[string]any
	json.Unmarshal(rec.Body.Bytes(), &task)
	row, _ := e.st.GetVideoTask(context.Background(), task["id"].(string))
	e.st.DeleteAccount(context.Background(), row.AccountID)
	time.Sleep(1100 * time.Millisecond)
	rec = e.do(httptest.NewRequest("GET", "/v1/videos/"+task["id"].(string), nil))
	var obj map[string]any
	json.Unmarshal(rec.Body.Bytes(), &obj)
	if obj["status"] != "failed" || !strings.Contains(rec.Body.String(), "deleted") {
		t.Fatalf("obj: %v", obj)
	}
}

func TestVideoContentRange(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	content := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "v.mp4", time.Now(), bytes.NewReader([]byte("0123456789")))
	}))
	t.Cleanup(content.Close)
	ctx := context.Background()
	task := &store.VideoTask{
		ID: "video_x", ModelID: "vid", ProviderID: "p1", AccountID: "acc-a",
		Handle: media.TaskHandle(`{}`), Status: media.TaskCompleted, Progress: 100, ResultURL: content.URL + "/v.mp4",
	}
	e.st.CreateVideoTask(ctx, task)
	now := time.Now()
	task.CompletedAt = &now
	e.st.UpdateVideoTask(ctx, task)

	req := httptest.NewRequest("GET", "/v1/videos/video_x/content", nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := e.do(req)
	if rec.Code != 206 || rec.Body.String() != "0123" {
		t.Fatalf("content: %d %q", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Range") == "" {
		t.Fatal("no Content-Range")
	}
	// not-ready → 409
	task2 := &store.VideoTask{ID: "video_y", ModelID: "vid", ProviderID: "p1", AccountID: "acc-a",
		Handle: media.TaskHandle(`{}`), Status: media.TaskQueued}
	e.st.CreateVideoTask(ctx, task2)
	rec = e.do(httptest.NewRequest("GET", "/v1/videos/video_y/content", nil))
	if rec.Code != 409 {
		t.Fatalf("not ready: %d", rec.Code)
	}
}

func TestImagesGenerationAndEdit(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.imgFn = func(c provider.Call, r media.ImageRequest) (*media.ImageResult, error) {
		return &media.ImageResult{Images: []media.Asset{{URL: "https://cdn/i1.png"}}}, nil
	}
	rec := e.jsonReq("POST", "/v1/images/generations", map[string]any{"model": "img", "prompt": "p", "n": 2})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Created int64 `json:"created"`
		Data    []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Created == 0 || len(out.Data) != 1 || out.Data[0].URL != "https://cdn/i1.png" {
		t.Fatalf("resp: %s", rec.Body)
	}
	// prompt required
	if rec := e.jsonReq("POST", "/v1/images/generations", map[string]any{"model": "img"}); rec.Code != 400 {
		t.Fatalf("no prompt: %d", rec.Code)
	}
	// n max 10
	if rec := e.jsonReq("POST", "/v1/images/generations", map[string]any{"model": "img", "prompt": "p", "n": 11}); rec.Code != 400 {
		t.Fatalf("n=11: %d", rec.Code)
	}

	// multipart edit carries image + mask and sets Edit
	var gotReq media.ImageRequest
	fake.imgFn = func(c provider.Call, r media.ImageRequest) (*media.ImageResult, error) {
		gotReq = r
		return &media.ImageResult{Images: []media.Asset{{URL: "u"}}}, nil
	}
	req := multipartReq(t, "/v1/images/edits",
		map[string]string{"model": "img", "prompt": "make it blue", "extra": `{"strength":0.5}`},
		map[string][][]byte{"image": {[]byte("IMG")}, "mask": {[]byte("MSK")}})
	rec = e.do(req)
	if rec.Code != 200 || !gotReq.Edit || len(gotReq.Images) != 1 || gotReq.Mask == nil || gotReq.Extra["strength"] != 0.5 {
		t.Fatalf("edit: %d %+v", rec.Code, gotReq)
	}
}

func TestImagesB64Conversion(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("PNGDATA"))
	}))
	t.Cleanup(file.Close)
	fake.imgFn = func(c provider.Call, r media.ImageRequest) (*media.ImageResult, error) {
		return &media.ImageResult{Images: []media.Asset{
			{URL: file.URL + "/a.png"},
			{B64: "QUJD"},
		}}, nil
	}
	rec := e.jsonReq("POST", "/v1/images/generations", map[string]any{
		"model": "img", "prompt": "p", "response_format": "b64_json",
	})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Data) != 2 || out.Data[0]["b64_json"] != "UE5HREFUQQ==" || out.Data[1]["b64_json"] != "QUJD" {
		t.Fatalf("data: %v", out.Data)
	}
}

func TestSpeechAndFailover(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.speakFn = func(c provider.Call, r media.SpeechRequest) (*media.Audio, error) {
		if c.Account.APIKey == "key-A" {
			return nil, &media.UpstreamError{Class: media.ClassQuota, Status: 402, Message: "no balance"}
		}
		return &media.Audio{Body: io.NopCloser(strings.NewReader("AUDIO")), ContentType: "audio/mp3"}, nil
	}
	rec := e.jsonReq("POST", "/v1/audio/speech", map[string]any{"model": "tts", "input": "hello", "voice": "v1"})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Type") != "audio/mp3" || rec.Body.String() != "AUDIO" {
		t.Fatalf("speech: %q %q", rec.Header().Get("Content-Type"), rec.Body)
	}
	logs, _ := e.st.ListRequestLogs(context.Background(), "", time.Time{}, 5)
	if logs[0].Attempts != 2 || logs[0].ModelID != "tts" {
		t.Fatalf("log: %+v", logs[0])
	}
	// input required
	if rec := e.jsonReq("POST", "/v1/audio/speech", map[string]any{"model": "tts"}); rec.Code != 400 {
		t.Fatalf("no input: %d", rec.Code)
	}
}

func TestUnsupportedKind(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.st.CreateProvider(ctx, &store.Provider{ID: "pimg", Type: "fakeimg", Enabled: true})
	e.account(t, "acc-i", "pimg", "key-I")
	e.st.CreateModel(ctx, &store.Model{ID: "vimg", ProviderID: "pimg", Kind: media.KindVideo, Upstream: "up", Enabled: true})
	rec := e.jsonReq("POST", "/v1/videos", map[string]any{"model": "vimg", "prompt": "p"})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "does not support video") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestVideoListEndpoint(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.submitFn = func(c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
		return media.TaskHandle(`{}`), nil
	}
	e.jsonReq("POST", "/v1/videos", map[string]any{"model": "vid", "prompt": "p"})
	rec := e.do(httptest.NewRequest("GET", "/v1/videos", nil))
	var out struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Object != "list" || len(out.Data) != 1 {
		t.Fatalf("list: %v", out)
	}
}

// A failed request still records the last attempted account in request_logs.
func TestFailedRequestLogsAccountID(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.imgFn = func(c provider.Call, r media.ImageRequest) (*media.ImageResult, error) {
		return nil, &media.UpstreamError{Class: media.ClassInvalid, Status: 400, Message: "bad prompt"}
	}
	rec := e.jsonReq("POST", "/v1/images/generations", map[string]any{"model": "img", "prompt": "x"})
	if rec.Code != 400 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	logs, err := e.st.ListRequestLogs(context.Background(), "", time.Now().Add(-time.Minute), 10)
	if err != nil || len(logs) == 0 {
		t.Fatalf("logs: %v %v", logs, err)
	}
	if logs[0].AccountID != "acc-a" || logs[0].Status != 400 {
		t.Fatalf("log: %+v", logs[0])
	}
}
