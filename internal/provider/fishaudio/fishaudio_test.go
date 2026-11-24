package fishaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type captured struct {
	model   string
	auth    string
	body    map[string]any
	status  int
	resp    []byte
	headers map[string]string
}

func fakeFish(t *testing.T, cap *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.model = r.Header.Get("model")
		cap.auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&cap.body)
		for k, v := range cap.headers {
			w.Header().Set(k, v)
		}
		if cap.status == 0 {
			cap.status = 200
		}
		w.WriteHeader(cap.status)
		w.Write(cap.resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func call(srv *httptest.Server) provider.Call {
	return provider.Call{
		Provider: provider.ProviderConfig{BaseURL: srv.URL},
		Account:  provider.AccountConfig{APIKey: "fish-key"},
		Model:    provider.ModelConfig{Upstream: "s2-pro"},
		HTTP:     srv.Client(),
	}
}

func TestSpeakBasics(t *testing.T) {
	cap := &captured{resp: []byte("audio!"), headers: map[string]string{"Content-Type": "audio/wav"}}
	srv := fakeFish(t, cap)
	c := call(srv)
	c.Model.Options = json.RawMessage(`{"voices":{"alice":"ref-77"}}`)
	a, err := Adapter{}.Speak(context.Background(), c, media.SpeechRequest{Input: "hi", Voice: "alice", Format: "wav", Speed: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Body.Close()
	b, _ := io.ReadAll(a.Body)
	if string(b) != "audio!" || a.ContentType != "audio/wav" {
		t.Fatalf("%q %q", b, a.ContentType)
	}
	if cap.model != "s2-pro" || cap.auth != "Bearer fish-key" {
		t.Fatalf("headers: %q %q", cap.model, cap.auth)
	}
	if cap.body["reference_id"] != "ref-77" || cap.body["format"] != "wav" || cap.body["text"] != "hi" {
		t.Fatalf("body: %v", cap.body)
	}
	prosody, _ := cap.body["prosody"].(map[string]any)
	if prosody["speed"] != 1.5 {
		t.Fatalf("prosody: %v", cap.body["prosody"])
	}
}

func TestFormatAndVoiceOptional(t *testing.T) {
	cap := &captured{resp: []byte("x")}
	srv := fakeFish(t, cap)
	_, err := Adapter{}.Speak(context.Background(), call(srv), media.SpeechRequest{Input: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if cap.body["format"] != "mp3" {
		t.Fatalf("default format: %v", cap.body)
	}
	if _, ok := cap.body["reference_id"]; ok {
		t.Fatal("reference_id should be omitted when voice empty")
	}
	_, err = Adapter{}.Speak(context.Background(), call(srv), media.SpeechRequest{Input: "t", Format: "aac"})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || ue.Class != media.ClassInvalid {
		t.Fatalf("format: %v", err)
	}
}

func TestErrorMapping(t *testing.T) {
	for st, class := range map[int]media.ErrorClass{
		401: media.ClassAuth, 403: media.ClassAuth, 402: media.ClassQuota,
		429: media.ClassRateLimit, 400: media.ClassInvalid, 500: media.ClassUpstream,
	} {
		cap := &captured{status: st, resp: []byte(`{"message":"m"}`)}
		srv := fakeFish(t, cap)
		_, err := Adapter{}.Speak(context.Background(), call(srv), media.SpeechRequest{Input: "t"})
		var ue *media.UpstreamError
		if !errors.As(err, &ue) || ue.Class != class {
			t.Fatalf("%d: %v want %v", st, err, class)
		}
	}
}

func TestListModels(t *testing.T) {
	page, err := Adapter{}.ListModels(context.Background(), provider.Call{}, provider.ModelQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Models) != 6 {
		t.Fatalf("models: %+v", page.Models)
	}
	m0 := page.Models[0]
	if m0.ID != "s2.1-pro" || m0.Kind != media.KindSpeech || m0.Category != "text-to-speech" {
		t.Fatalf("first: %+v", m0)
	}
	last := page.Models[len(page.Models)-1]
	if last.ID != "s1" || !last.Deprecated {
		t.Fatalf("last: %+v", last)
	}
	page, err = Adapter{}.ListModels(context.Background(), provider.Call{}, provider.ModelQuery{Q: "s3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Models) != 1 || page.Models[0].ID != "s3-pro" {
		t.Fatalf("q filter: %+v", page.Models)
	}
}

func TestListVoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("self") != "true" {
			t.Errorf("self: %q", r.URL.Query().Get("self"))
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth: %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"total":3,"items":[
			{"_id":"v-trained","type":"tts","title":"鲸鱼娘旁白","state":"trained","visibility":"public","languages":["zh"]},
			{"_id":"v-training","type":"tts","title":"未训练好","state":"training"},
			{"_id":"v-svc","type":"svc","title":"服务模型","state":"trained"}
		]}`)
	}))
	defer srv.Close()

	c := provider.Call{
		Provider: provider.ProviderConfig{ID: "fa", Type: "fishaudio", BaseURL: srv.URL},
		Account:  provider.AccountConfig{ID: "a1", APIKey: "k"},
		HTTP:     srv.Client(),
	}
	out, err := Adapter{}.ListVoices(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	// Only the trained tts voice is usable as reference_id.
	if len(out) != 1 {
		t.Fatalf("want 1 usable voice, got %d: %+v", len(out), out)
	}
	if out[0].ID != "v-trained" || out[0].Name != "鲸鱼娘旁白" {
		t.Fatalf("voice: %+v", out[0])
	}
	if !strings.Contains(out[0].Detail, "public") || !strings.Contains(out[0].Detail, "zh") {
		t.Fatalf("detail: %q", out[0].Detail)
	}
}

// TestSpeakZeroShotCloning verifies a reference sample is embedded as a
// msgpack references[] payload instead of a reference_id lookup.
func TestSpeakZeroShotCloning(t *testing.T) {
	var (
		gotCT    string
		gotBody  []byte
		gotModel string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotModel = r.Header.Get("model")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "audio/mpeg")
		io.WriteString(w, "mp3")
	}))
	defer srv.Close()

	sample := []byte{0xff, 0xfb, 0x90, 0x00}
	c := provider.Call{
		Provider: provider.ProviderConfig{ID: "fa", Type: "fishaudio", BaseURL: srv.URL},
		Account:  provider.AccountConfig{ID: "a1", APIKey: "k"},
		Model:    provider.ModelConfig{Upstream: "s2.1-pro-free"},
		HTTP:     srv.Client(),
	}
	_, err := Adapter{}.Speak(context.Background(), c, media.SpeechRequest{
		Input:          "hi",
		Format:         "mp3",
		ReferenceAudio: &media.Input{Name: "s.mp3", Data: sample},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotCT != "application/msgpack" {
		t.Fatalf("content type: %q", gotCT)
	}
	if gotModel != "s2.1-pro-free" {
		t.Fatalf("model header: %q", gotModel)
	}
	// fixmap(3): "text" "hi" | "references" [ map(2): "audio" bin8{sample} "text" "" ] | "format" "mp3"
	want := []byte{
		0x83,
		0xa4, 't', 'e', 'x', 't', 0xa2, 'h', 'i',
		0xaa, 'r', 'e', 'f', 'e', 'r', 'e', 'n', 'c', 'e', 's',
		0x91, 0x82,
		0xa5, 'a', 'u', 'd', 'i', 'o', 0xc4, 0x04, 0xff, 0xfb, 0x90, 0x00,
		0xa4, 't', 'e', 'x', 't', 0xa0,
		0xa6, 'f', 'o', 'r', 'm', 'a', 't', 0xa3, 'm', 'p', '3',
	}
	if !bytes.Equal(gotBody, want) {
		t.Fatalf("body mismatch\n got %x\nwant %x", gotBody, want)
	}
}

// TestSpeakReferenceIDUnchanged guards the plain path: no sample means a JSON
// body with reference_id, exactly as before cloning was added.
func TestSpeakReferenceIDUnchanged(t *testing.T) {
	var gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "audio/mpeg")
		io.WriteString(w, "mp3")
	}))
	defer srv.Close()

	c := provider.Call{
		Provider: provider.ProviderConfig{ID: "fa", Type: "fishaudio", BaseURL: srv.URL},
		Account:  provider.AccountConfig{ID: "a1", APIKey: "k"},
		HTTP:     srv.Client(),
	}
	if _, err := (Adapter{}).Speak(context.Background(), c, media.SpeechRequest{
		Input: "hi", Format: "mp3", Voice: "ref-1", VoiceResolved: true,
	}); err != nil {
		t.Fatal(err)
	}
	if gotCT != "application/json" {
		t.Fatalf("content type: %q", gotCT)
	}
	if gotBody["reference_id"] != "ref-1" {
		t.Fatalf("body: %v", gotBody)
	}
}

// TestSpeedClamped covers Fish's documented 0.5–2.0 range: out-of-range
// values must be clamped rather than sent and rejected.
func TestSpeedClamped(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0.2, 0.5}, {0.5, 0.5}, {1.0, 1.0}, {2.0, 2.0}, {3.5, 2.0},
	} {
		var got map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&got)
			w.Header().Set("Content-Type", "audio/mpeg")
			io.WriteString(w, "x")
		}))
		cc := provider.Call{
			Provider: provider.ProviderConfig{ID: "fa", Type: "fishaudio", BaseURL: srv.URL},
			Account:  provider.AccountConfig{ID: "a1", APIKey: "k"},
			HTTP:     srv.Client(),
		}
		(Adapter{}).Speak(context.Background(), cc, media.SpeechRequest{Input: "hi", Speed: c.in, Voice: "vm", VoiceResolved: true})
		srv.Close()
		pr, _ := got["prosody"].(map[string]any)
		if pr["speed"] != c.want {
			t.Fatalf("speed %v → %v, want %v", c.in, pr["speed"], c.want)
		}
	}
}

// TestLanguageForwarded checks language reaches the request when set.
func TestLanguageForwarded(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "audio/mpeg")
		io.WriteString(w, "x")
	}))
	defer srv.Close()
	c := provider.Call{
		Provider: provider.ProviderConfig{ID: "fa", Type: "fishaudio", BaseURL: srv.URL},
		Account:  provider.AccountConfig{ID: "a1", APIKey: "k"},
		HTTP:     srv.Client(),
	}
	(Adapter{}).Speak(context.Background(), c, media.SpeechRequest{Input: "hi", Speed: 1, Language: "zh-CN", Voice: "vm", VoiceResolved: true})
	if got["language"] != "zh-CN" {
		t.Fatalf("language not forwarded: %v", got)
	}
}
