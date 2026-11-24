package elevenlabs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

type captured struct {
	path        string
	paths       []string
	query       url.Values
	apiKey      string
	body        map[string]any
	status      int
	respBody    []byte
	respHeaders map[string]string
	// voicesPath is the response for GET /v1/voices (JSON), empty→`{"voices":[]}`.
	voicesPath []byte
}

func fakeEL(t *testing.T, cap *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.paths = append(cap.paths, r.URL.Path)
		cap.query = r.URL.Query()
		cap.apiKey = r.Header.Get("xi-api-key")
		if r.URL.Path == "/v1/voices" {
			body := cap.voicesPath
			if len(body) == 0 {
				body = []byte(`{"voices":[]}`)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&cap.body)
		for k, v := range cap.respHeaders {
			w.Header().Set(k, v)
		}
		if cap.status == 0 {
			cap.status = 200
		}
		w.WriteHeader(cap.status)
		w.Write(cap.respBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func call(srv *httptest.Server, upstream string) provider.Call {
	return provider.Call{
		Provider: provider.ProviderConfig{BaseURL: srv.URL},
		Account:  provider.AccountConfig{APIKey: "el-key"},
		Model:    provider.ModelConfig{Upstream: upstream},
		HTTP:     srv.Client(),
	}
}

func TestSpeakBasics(t *testing.T) {
	cap := &captured{respBody: []byte("mp3-bytes"), respHeaders: map[string]string{"Content-Type": "audio/mpeg"}}
	srv := fakeEL(t, cap)
	c := call(srv, "eleven_multilingual_v2")
	c.Model.Options = json.RawMessage(`{"voices":{"narr":"voice-id-1"}}`)
	a, err := Adapter{}.Speak(context.Background(), c, media.SpeechRequest{Input: "hi", Voice: "narr"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Body.Close()
	b, _ := io.ReadAll(a.Body)
	if string(b) != "mp3-bytes" || a.ContentType != "audio/mpeg" {
		t.Fatalf("audio: %q %q", b, a.ContentType)
	}
	if cap.path != "/v1/text-to-speech/voice-id-1" {
		t.Fatalf("path: %s", cap.path)
	}
	if cap.query.Get("output_format") != "mp3_44100_128" {
		t.Fatalf("format: %v", cap.query)
	}
	if cap.apiKey != "el-key" || cap.body["model_id"] != "eleven_multilingual_v2" || cap.body["text"] != "hi" {
		t.Fatalf("req: %v key=%q", cap.body, cap.apiKey)
	}
}

func TestVoiceDefaultAndChain(t *testing.T) {
	cap := &captured{respBody: []byte("x")}
	srv := fakeEL(t, cap)
	// default_voice from provider options (lowest precedence still found)
	c := call(srv, "m")
	c.Provider.Options = json.RawMessage(`{"default_voice":"p-voice"}`)
	if _, err := (Adapter{}).Speak(context.Background(), c, media.SpeechRequest{Input: "t"}); err != nil {
		t.Fatal(err)
	}
	if cap.path != "/v1/text-to-speech/p-voice" {
		t.Fatalf("path: %s", cap.path)
	}
	// empty library + nothing configured → Invalid
	c = call(srv, "m")
	_, err := Adapter{}.Speak(context.Background(), c, media.SpeechRequest{Input: "t"})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || ue.Class != media.ClassInvalid {
		t.Fatalf("err: %v", err)
	}
}

func TestVoiceAutoPick(t *testing.T) {
	cap := &captured{
		respBody:   []byte("x"),
		voicesPath: []byte(`{"voices":[{"voice_id":"auto-1","name":"Auto One"},{"voice_id":"auto-2"}]}`),
	}
	srv := fakeEL(t, cap)
	c := call(srv, "m")
	c.Account.ID = "acc-auto"
	defaultVoiceCache.Delete(srv.URL + "|acc-auto")
	if _, err := (Adapter{}).Speak(context.Background(), c, media.SpeechRequest{Input: "t"}); err != nil {
		t.Fatal(err)
	}
	if cap.path != "/v1/text-to-speech/auto-1" {
		t.Fatalf("path: %s (want first library voice)", cap.path)
	}
	if len(cap.paths) != 2 || cap.paths[0] != "/v1/voices" {
		t.Fatalf("lookup order: %v", cap.paths)
	}
	// second call reuses the cached id: no extra /v1/voices hit
	cap.paths = nil
	if _, err := (Adapter{}).Speak(context.Background(), c, media.SpeechRequest{Input: "t"}); err != nil {
		t.Fatal(err)
	}
	if len(cap.paths) != 1 || cap.paths[0] != "/v1/text-to-speech/auto-1" {
		t.Fatalf("cached call: %v", cap.paths)
	}
	// an explicit OpenAI-style name is passed through, never resolved
	cap.paths = nil
	if _, err := (Adapter{}).Speak(context.Background(), c, media.SpeechRequest{Input: "t", Voice: "alloy"}); err != nil {
		t.Fatal(err)
	}
	if len(cap.paths) != 1 || cap.paths[0] != "/v1/text-to-speech/alloy" {
		t.Fatalf("passthrough call: %v", cap.paths)
	}
}

func TestFormatMappingAndInvalid(t *testing.T) {
	for f, want := range map[string]string{
		"opus": "opus_48000_128", "pcm": "pcm_24000", "wav": "wav_44100",
	} {
		cap := &captured{respBody: []byte("x")}
		srv := fakeEL(t, cap)
		c := call(srv, "m")
		c.Model.Options = json.RawMessage(`{"default_voice":"v"}`)
		if _, err := (Adapter{}).Speak(context.Background(), c, media.SpeechRequest{Input: "t", Format: f}); err != nil {
			t.Fatal(err)
		}
		if cap.query.Get("output_format") != want {
			t.Fatalf("%s → %v", f, cap.query)
		}
	}
	cap := &captured{respBody: []byte("x")}
	srv := fakeEL(t, cap)
	c := call(srv, "m")
	c.Model.Options = json.RawMessage(`{"default_voice":"v"}`)
	_, err := Adapter{}.Speak(context.Background(), c, media.SpeechRequest{Input: "t", Format: "flac"})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || ue.Class != media.ClassInvalid {
		t.Fatalf("err: %v", err)
	}
}

func TestSpeedClampAndMerge(t *testing.T) {
	cap := &captured{respBody: []byte("x")}
	srv := fakeEL(t, cap)
	c := call(srv, "m")
	c.Model.Options = json.RawMessage(`{"default_voice":"v"}`)
	_, err := Adapter{}.Speak(context.Background(), c, media.SpeechRequest{
		Input: "t", Speed: 2.0,
		Extra: map[string]any{"voice_settings": map[string]any{"stability": 0.5}, "seed": 9},
	})
	if err != nil {
		t.Fatal(err)
	}
	vs, _ := cap.body["voice_settings"].(map[string]any)
	if vs["speed"] != 1.2 || vs["stability"] != 0.5 {
		t.Fatalf("voice_settings: %v", vs)
	}
	if cap.body["seed"] != float64(9) {
		t.Fatalf("extra: %v", cap.body)
	}
}

func TestErrorMapping(t *testing.T) {
	// 401 with quota_exceeded detail → Quota
	cap := &captured{status: 401, respBody: []byte(`{"detail":{"status":"quota_exceeded","message":"out"}}`)}
	srv := fakeEL(t, cap)
	c := call(srv, "m")
	c.Model.Options = json.RawMessage(`{"default_voice":"v"}`)
	_, err := Adapter{}.Speak(context.Background(), c, media.SpeechRequest{Input: "t"})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || ue.Class != media.ClassQuota {
		t.Fatalf("quota: %v", err)
	}
	// plain 401 → Auth; 429 → RateLimit; 500 → Upstream
	for st, class := range map[int]media.ErrorClass{401: media.ClassAuth, 429: media.ClassRateLimit, 500: media.ClassUpstream, 422: media.ClassInvalid} {
		cap := &captured{status: st, respBody: []byte(`{"detail":{"status":"x","message":"m"}}`)}
		srv := fakeEL(t, cap)
		c2 := call(srv, "m")
		c2.Model.Options = json.RawMessage(`{"default_voice":"v"}`)
		_, err := Adapter{}.Speak(context.Background(), c2, media.SpeechRequest{Input: "t"})
		if !errors.As(err, &ue) || ue.Class != class {
			t.Fatalf("%d: %v want %v", st, err, class)
		}
	}
}

func TestListModels(t *testing.T) {
	cap := &captured{respBody: []byte(`[
		{"model_id":"eleven_multilingual_v2","name":"Eleven Multilingual v2","description":"d1","can_do_text_to_speech":true},
		{"model_id":"eleven_turbo_v2_5","name":"Eleven Turbo v2.5","can_do_text_to_speech":true},
		{"model_id":"scribe_v1","name":"Scribe v1","can_do_text_to_speech":false,"can_do_speech_to_text":true}
	]`)}
	srv := fakeEL(t, cap)
	page, err := Adapter{}.ListModels(context.Background(), call(srv, ""), provider.ModelQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if cap.apiKey != "el-key" || cap.path != "/v1/models" {
		t.Fatalf("captured: path=%q key=%q", cap.path, cap.apiKey)
	}
	if len(page.Models) != 2 { // scribe_v1 is not TTS-capable
		t.Fatalf("models: %+v", page.Models)
	}
	m0 := page.Models[0]
	if m0.ID != "eleven_multilingual_v2" || m0.Kind != media.KindSpeech ||
		m0.Category != "text-to-speech" || m0.Description != "d1" || m0.Name != "Eleven Multilingual v2" {
		t.Fatalf("model[0]: %+v", m0)
	}
	// q filters by id or name, case-insensitive
	page, err = Adapter{}.ListModels(context.Background(), call(srv, ""), provider.ModelQuery{Q: "TURBO"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Models) != 1 || page.Models[0].ID != "eleven_turbo_v2_5" {
		t.Fatalf("q filter: %+v", page.Models)
	}
}
