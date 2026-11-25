package openai_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/store"
)

// speakProbe posts a speech request and reports the status and whether the
// adapter was reached.
func speakProbe(t *testing.T, e *env, voice string) (int, string, bool) {
	t.Helper()
	spoken := false
	fake.speakFn = func(_ provider.Call, _ media.SpeechRequest) (*media.Audio, error) {
		spoken = true
		return &media.Audio{Body: io.NopCloser(strings.NewReader("A")), ContentType: "audio/mpeg"}, nil
	}
	body := map[string]any{"model": "tts", "input": "hi"}
	if voice != "" {
		body["voice"] = voice
	}
	rec := e.jsonReq("POST", "/v1/audio/speech", body)
	return rec.Code, rec.Body.String(), spoken
}

func setModelOptions(t *testing.T, e *env, id, opts string) {
	t.Helper()
	raw := json.RawMessage(opts)
	if err := e.st.UpdateModel(context.Background(), id, store.ModelPatch{Options: &raw}); err != nil {
		t.Fatal(err)
	}
}

type voiceCase struct {
	voice   string
	ok      bool
	errPart string
}

func runVoiceCases(t *testing.T, e *env, cases []voiceCase) {
	t.Helper()
	for _, c := range cases {
		code, body, spoken := speakProbe(t, e, c.voice)
		if c.ok {
			if code != 200 || !spoken {
				t.Errorf("voice %q: want pass-through, got %d spoken=%v %s", c.voice, code, spoken, body)
			}
			continue
		}
		if code != 400 || spoken {
			t.Errorf("voice %q: want 400 before upstream, got %d spoken=%v %s", c.voice, code, spoken, body)
			continue
		}
		if c.errPart != "" && !strings.Contains(body, c.errPart) {
			t.Errorf("voice %q: error should mention %q: %s", c.voice, c.errPart, body)
		}
	}
}

// Library mode (ElevenLabs/Fish): OpenAI names are rejected, empty keeps the
// adapter default, vendor ids and configured aliases pass.
func TestCheckVoiceLibrary(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	runVoiceCases(t, e, []voiceCase{
		{voice: "alloy", errPart: "alloy"},
		{voice: "Nova", errPart: "Nova"},
		{voice: "", ok: true},
		{voice: "AcO3OWvendor", ok: true},
	})
	setModelOptions(t, e, "tts", `{"voices":{"alloy":"vendor-alloy"}}`)
	runVoiceCases(t, e, []voiceCase{{voice: "alloy", ok: true}})
}

// Preset mode with a declared list (MiMo): only listed names pass; the error
// lists the presets.
func TestCheckVoicePreset(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	setModelOptions(t, e, "tts", `{"voice_mode":"preset","voice_presets":["冰糖","茉莉"]}`)
	runVoiceCases(t, e, []voiceCase{
		{voice: "alloy", errPart: "冰糖"},
		{voice: "苏打", errPart: "茉莉"},
		{voice: "冰糖", ok: true},
		{voice: "", ok: true},
	})
}

// Sample mode (voice clone): no sample means no request.
func TestCheckVoiceSample(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	setModelOptions(t, e, "tts", `{"voice_mode":"sample"}`)
	runVoiceCases(t, e, []voiceCase{
		{voice: "", errPart: "克隆"},
		{voice: "alloy", errPart: "样本"},
	})
}

// Prompt mode (voice design): a description is mandatory.
func TestCheckVoicePrompt(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	setModelOptions(t, e, "tts", `{"voice_mode":"prompt"}`)
	runVoiceCases(t, e, []voiceCase{
		{voice: "", errPart: "音色描述"},
		{voice: "alloy", errPart: "音色描述"},
		{voice: "温柔的年轻女声", ok: true},
	})
}

// requires_images: a prompt-only request never reaches SubmitVideo.
func TestVideoRequiresImages(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	setModelOptions(t, e, "vid", `{"requires_images":true}`)
	fake.videoReqs = nil
	fake.submitFn = func(c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
		return media.TaskHandle(`{"h":"1"}`), nil
	}
	rec := e.do(multipartReq(t, "/v1/videos", map[string]string{"model": "vid", "prompt": "a cat"}, nil))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "图生视频") {
		t.Fatalf("prompt-only: want 400, got %d %s", rec.Code, rec.Body)
	}
	if len(fake.videoReqs) != 0 {
		t.Fatal("prompt-only request reached the adapter")
	}
	rec = e.do(multipartReq(t, "/v1/videos", map[string]string{"model": "vid", "prompt": "a cat"},
		map[string][][]byte{"input_reference": {[]byte("FF")}}))
	if rec.Code != 200 || len(fake.videoReqs) != 1 {
		t.Fatalf("with image: %d %s reqs=%d", rec.Code, rec.Body, len(fake.videoReqs))
	}
}

// Plural multipart names (images[]/videos[]/audios[]) mirror the JSON keys and
// must not be dropped silently.
func TestVideoMultipartPluralNames(t *testing.T) {
	e := newEnv(t)
	seedVideo(t, e)
	fake.videoReqs = nil
	fake.submitFn = func(c provider.Call, r media.VideoRequest) (media.TaskHandle, error) {
		return media.TaskHandle(`{"h":"1"}`), nil
	}
	rec := e.do(multipartReq(t, "/v1/videos", map[string]string{"model": "vid", "prompt": "a cat"},
		map[string][][]byte{
			// Three images: two would be auto-split into first/last frames.
			"images[]": {[]byte("R1"), []byte("R2"), []byte("R3")},
			"videos[]": {[]byte("VD")},
			"audios":   {[]byte("AU")},
		}))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	r := fake.videoReqs[0]
	if len(r.References) != 3 || len(r.Videos) != 1 || len(r.Audios) != 1 {
		t.Fatalf("refs=%d vids=%d auds=%d", len(r.References), len(r.Videos), len(r.Audios))
	}
}
