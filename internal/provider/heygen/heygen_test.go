package heygen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

func call(base, model, key string) provider.Call {
	return provider.Call{
		Provider: provider.ProviderConfig{ID: "hg", Type: "heygen", BaseURL: base},
		Account:  provider.AccountConfig{ID: "a1", APIKey: key},
		Model:    provider.ModelConfig{Upstream: model},
		HTTP:     http.DefaultClient,
		Stream:   http.DefaultClient,
	}
}

// TestSpeakDownloadsAudioURL covers the two-request shape: HeyGen answers with
// a URL, which the adapter must then fetch for the bytes.
func TestSpeakDownloadsAudioURL(t *testing.T) {
	var gotBody map[string]any
	var gotKey string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/models/audio/tts":
			gotKey = r.Header.Get("X-Api-Key")
			json.NewDecoder(r.Body).Decode(&gotBody)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"audio_url": srv.URL + "/file.mp3", "duration": 1.2},
			})
		case "/file.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
			io.WriteString(w, "MP3BYTES")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a, err := Adapter{}.Speak(context.Background(), call(srv.URL, "heygen-voice-1-turbo-free", "k"), media.SpeechRequest{
		Input: "hi", Format: "mp3", Voice: "6c36", VoiceResolved: true, Speed: 1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Body.Close()
	b, _ := io.ReadAll(a.Body)
	if string(b) != "MP3BYTES" || a.ContentType != "audio/mpeg" {
		t.Fatalf("audio: %q %q", b, a.ContentType)
	}
	if gotKey != "k" {
		t.Fatalf("api key header: %q", gotKey)
	}
	if gotBody["voice_id"] != "6c36" || gotBody["model"] != "heygen-voice-1-turbo-free" {
		t.Fatalf("body: %v", gotBody)
	}
	// Turbo models must not receive a speed field.
	if _, has := gotBody["speed"]; has {
		t.Fatalf("turbo model should not get speed: %v", gotBody)
	}
	of, _ := gotBody["output_format"].(map[string]any)
	if of["encoding"] != "mp3" {
		t.Fatalf("encoding: %v", of)
	}
}

func TestSpeakSpeedForNonTurbo(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/models/audio/tts" {
			json.NewDecoder(r.Body).Decode(&gotBody)
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"audio_url": "http://127.0.0.1:1/x"}})
			return
		}
	}))
	defer srv.Close()
	// The download will fail; we only care that speed reached the request.
	// 1.5 is outside HeyGen's documented 0.75–1.25 and must be clamped.
	Adapter{}.Speak(context.Background(), call(srv.URL, "heygen-voice-1", "k"), media.SpeechRequest{
		Input: "hi", Format: "mp3", Speed: 1.5, Voice: "v1", VoiceResolved: true,
	})
	if gotBody["speed"] != 1.25 {
		t.Fatalf("speed should be clamped to 1.25, got %v", gotBody["speed"])
	}
}

func TestSpeakFormatMapping(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"wav", "pcm"}, {"pcm", "pcm"}, {"mp3", "mp3"}, {"", "mp3"},
	} {
		var got map[string]any
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v3/models/audio/tts" {
				json.NewDecoder(r.Body).Decode(&got)
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"audio_url": srv.URL + "/f"}})
				return
			}
			io.WriteString(w, "x")
		}))
		Adapter{}.Speak(context.Background(), call(srv.URL, "m", "k"), media.SpeechRequest{Input: "hi", Format: c.in, Voice: "v1", VoiceResolved: true})
		srv.Close()
		of, _ := got["output_format"].(map[string]any)
		if of["encoding"] != c.want {
			t.Fatalf("%q → %v, want %s", c.in, of, c.want)
		}
	}
}

func TestSpeakErrorClass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"auth","message":"bad key"}}`)
	}))
	defer srv.Close()
	_, err := Adapter{}.Speak(context.Background(), call(srv.URL, "m", "k"), media.SpeechRequest{Input: "hi", Voice: "v1", VoiceResolved: true})
	ue, ok := err.(*media.UpstreamError)
	if !ok || ue.Class != media.ClassAuth {
		t.Fatalf("err: %v", err)
	}
}

// TestSpeakRequiresVoiceID locks the guard: HeyGen rejects a request without
// voice_id, so the adapter must say so plainly instead of forwarding the
// vendor's generic validation error.
func TestSpeakRequiresVoiceID(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		io.WriteString(w, `{"data":{"audio_url":"x"}}`)
	}))
	defer srv.Close()
	_, err := Adapter{}.Speak(context.Background(), call(srv.URL, "heygen-voice-1", "k"), media.SpeechRequest{
		Input: "hi", Format: "mp3",
	})
	if err == nil {
		t.Fatal("expected an error when voice_id is missing")
	}
	if called {
		t.Fatal("must not reach the vendor without a voice_id")
	}
	ue, ok := err.(*media.UpstreamError)
	if !ok || ue.Class != media.ClassInvalid {
		t.Fatalf("want ClassInvalid, got %v", err)
	}
}
