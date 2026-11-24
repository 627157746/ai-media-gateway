package fishaudio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
