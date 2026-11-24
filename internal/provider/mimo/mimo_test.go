package mimo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

// fakeTTS echoes a fixed base64 payload and captures the request body.
func fakeTTS(t *testing.T, got *map[string]any, payload []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.Header.Get("api-key") != "k" {
			t.Errorf("api-key header: %q", r.Header.Get("api-key"))
		}
		json.NewDecoder(r.Body).Decode(got)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"audio": map[string]any{"data": base64.StdEncoding.EncodeToString(payload)},
				},
			}},
		})
	}))
}

func call(base, model string) provider.Call {
	return provider.Call{
		Provider: provider.ProviderConfig{ID: "mm", Type: "mimo", BaseURL: base},
		Account:  provider.AccountConfig{ID: "a1", APIKey: "k"},
		Model:    provider.ModelConfig{Upstream: model},
		HTTP:     http.DefaultClient,
	}
}

// TestSpeakPlain checks the preset-voice mode and base64 decoding.
func TestSpeakPlain(t *testing.T) {
	var got map[string]any
	srv := fakeTTS(t, &got, []byte("MP3DATA"))
	defer srv.Close()

	a, err := Adapter{}.Speak(context.Background(), call(srv.URL, "mimo-v2.5-tts"), media.SpeechRequest{
		Input: "hello", Format: "mp3", Voice: "冰糖", VoiceResolved: true, Instructions: "欢快",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Body.Close()
	b, _ := io.ReadAll(a.Body)
	if string(b) != "MP3DATA" {
		t.Fatalf("audio: %q", b)
	}
	audio, _ := got["audio"].(map[string]any)
	if audio["voice"] != "冰糖" || audio["format"] != "mp3" {
		t.Fatalf("audio field: %v", audio)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages: %v", msgs)
	}
	// The user turn carries the style prompt.
	if u, _ := msgs[0].(map[string]any); u["content"] != "欢快" {
		t.Fatalf("style prompt: %v", msgs[0])
	}
}

// TestSpeakDefaultsVoice ensures an empty voice falls back to mimo_default
// instead of sending an empty string upstream.
func TestSpeakDefaultsVoice(t *testing.T) {
	var got map[string]any
	srv := fakeTTS(t, &got, []byte("x"))
	defer srv.Close()
	if _, err := (Adapter{}).Speak(context.Background(), call(srv.URL, "mimo-v2.5-tts"), media.SpeechRequest{Input: "hi", Format: "mp3"}); err != nil {
		t.Fatal(err)
	}
	audio, _ := got["audio"].(map[string]any)
	if audio["voice"] != "mimo_default" {
		t.Fatalf("voice: %v", audio["voice"])
	}
}

// TestSpeakVoiceClone verifies a reference sample becomes a data URL.
func TestSpeakVoiceClone(t *testing.T) {
	var got map[string]any
	srv := fakeTTS(t, &got, []byte("x"))
	defer srv.Close()
	sample := []byte{0x01, 0x02, 0x03}
	_, err := Adapter{}.Speak(context.Background(), call(srv.URL, "mimo-v2.5-tts-voiceclone"), media.SpeechRequest{
		Input: "hi", Format: "mp3",
		ReferenceAudio: &media.Input{Data: sample, MIME: "audio/wav"},
	})
	if err != nil {
		t.Fatal(err)
	}
	audio, _ := got["audio"].(map[string]any)
	want := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(sample)
	if audio["voice"] != want {
		t.Fatalf("voice param: %v", audio["voice"])
	}
}

// TestSpeakVoiceDesign checks no voice field is sent and the design prompt
// replaces the style turn.
func TestSpeakVoiceDesign(t *testing.T) {
	var got map[string]any
	srv := fakeTTS(t, &got, []byte("x"))
	defer srv.Close()
	_, err := Adapter{}.Speak(context.Background(), call(srv.URL, "mimo-v2.5-tts-voicedesign"), media.SpeechRequest{
		Input: "hi", Format: "mp3", Voice: "甜美可爱的二次元少女",
	})
	if err != nil {
		t.Fatal(err)
	}
	audio, _ := got["audio"].(map[string]any)
	if _, has := audio["voice"]; has {
		t.Fatalf("design mode must not send voice: %v", audio)
	}
	msgs, _ := got["messages"].([]any)
	if u, _ := msgs[0].(map[string]any); u["content"] != "甜美可爱的二次元少女" {
		t.Fatalf("design prompt: %v", msgs[0])
	}
}

// TestSpeakMissingAudio guards against a success status carrying no audio.
func TestSpeakMissingAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{}}]}`)
	}))
	defer srv.Close()
	if _, err := (Adapter{}).Speak(context.Background(), call(srv.URL, "mimo-v2.5-tts"), media.SpeechRequest{Input: "hi"}); err == nil {
		t.Fatal("expected error for missing audio")
	}
}

// TestSpeedBecomesStylePrompt verifies MiMo has no speed field: the rate must be
// expressed in the user turn, which is the documented mechanism.
func TestSpeedBecomesStylePrompt(t *testing.T) {
	cases := []struct {
		speed float64
		want  string
	}{
		{0, "自然流畅，清晰传神"},   // no preference → neutral default
		{1.0, "自然流畅，清晰传神"}, // near 1.0 → still neutral
		{0.6, "语速放慢"},
		{1.4, "语速稍快"},
		{2.5, "语速明显加快"},
	}
	for _, c := range cases {
		var got map[string]any
		srv := fakeTTS(t, &got, []byte("x"))
		if _, err := (Adapter{}).Speak(context.Background(), call(srv.URL, "mimo-v2.5-tts"), media.SpeechRequest{Input: "hi", Speed: c.speed}); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		msgs, _ := got["messages"].([]any)
		if u, _ := msgs[0].(map[string]any); u["content"] != c.want {
			t.Fatalf("speed %v → %q, want %q", c.speed, u["content"], c.want)
		}
		// No numeric speed may leak into the body.
		if _, has := got["speed"]; has {
			t.Fatalf("mimo must not send a speed field: %v", got)
		}
	}
}

// TestSpeedMergesWithInstructions checks an explicit instruction is preserved
// and only extended.
func TestSpeedMergesWithInstructions(t *testing.T) {
	var got map[string]any
	srv := fakeTTS(t, &got, []byte("x"))
	defer srv.Close()
	(Adapter{}).Speak(context.Background(), call(srv.URL, "mimo-v2.5-tts"), media.SpeechRequest{
		Input: "hi", Speed: 0.6, Instructions: "温柔",
	})
	msgs, _ := got["messages"].([]any)
	if u, _ := msgs[0].(map[string]any); u["content"] != "温柔，语速放慢" {
		t.Fatalf("merged style: %v", u["content"])
	}
}
