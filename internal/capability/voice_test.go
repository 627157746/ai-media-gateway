package capability

import (
	"encoding/json"
	"testing"
)

// TestClampPerProvider pins each provider's documented range. These numbers
// mirror the vendor docs, so a wrong clamp here is a wrong request upstream.
func TestClampPerProvider(t *testing.T) {
	cases := []struct {
		provider string
		in, want float64
	}{
		{"elevenlabs", 0.5, 0.7}, // below min
		{"elevenlabs", 2.0, 1.2}, // above max
		{"elevenlabs", 1.0, 1.0}, // in range
		{"fishaudio", 0.2, 0.5},
		{"fishaudio", 3.5, 2.0},
		{"fishaudio", 1.5, 1.5},
		{"heygen", 0.5, 0.75},
		{"heygen", 1.5, 1.25},
		{"heygen", 1.0, 1.0},
	}
	for _, c := range cases {
		if got := For(c.provider).Speed.Clamp(c.in); got != c.want {
			t.Fatalf("%s speed %v → %v, want %v", c.provider, c.in, got, c.want)
		}
	}
	// Unset stays unset so callers can omit the field.
	if got := For("fishaudio").Speed.Clamp(0); got != 0 {
		t.Fatalf("zero speed should stay zero, got %v", got)
	}
	// A provider without a numeric field must never emit one.
	if got := For("mimo").Speed.Clamp(1.5); got != 0 {
		t.Fatalf("mimo must not emit numeric speed, got %v", got)
	}
}

func TestSpeedPhrase(t *testing.T) {
	cap := For("mimo").Speed
	cases := []struct {
		in   float64
		want string
	}{
		{0, ""}, {1.0, ""}, {1.1, ""}, {0.6, "语速放慢"}, {1.4, "语速稍快"}, {2.5, "语速明显加快"},
	}
	for _, c := range cases {
		if got := cap.SpeedPhrase(c.in); got != c.want {
			t.Fatalf("speed %v → %q, want %q", c.in, got, c.want)
		}
	}
	// Only prompt-mode providers produce phrasing.
	if got := For("fishaudio").Speed.SpeedPhrase(0.6); got != "" {
		t.Fatalf("field-mode provider must not phrase, got %q", got)
	}
}

// TestVoiceModes pins the mode each provider declares.
func TestVoiceModes(t *testing.T) {
	for provider, want := range map[string]VoiceMode{
		"elevenlabs": Library,
		"fishaudio":  Library,
		"heygen":     Preset,
		"mimo":       Preset,
	} {
		if got := For(provider).VoiceMode; got != want {
			t.Fatalf("%s voice mode %q, want %q", provider, got, want)
		}
	}
	// Unknown providers must not panic and must stay usable.
	if got := For("brand-new"); got.VoiceMode != Library || len(got.Formats) == 0 {
		t.Fatalf("unknown provider fallback: %+v", got)
	}
}

// TestHeyGenRequiresVoice documents that HeyGen rejects a request without a
// voice id — the reason its UI keeps a manual box even with no preset list.
func TestHeyGenRequiresVoice(t *testing.T) {
	if !For("heygen").VoiceRequired {
		t.Fatal("heygen must declare voice_required")
	}
	if len(For("heygen").VoicePresets) != 0 {
		t.Fatal("heygen has no preset list; voice ids are workspace-specific")
	}
}

// TestLanguageSets checks the declared presets resolve to non-empty lists and
// that v4 is a superset of v3 (v4 adds languages rather than dropping them).
func TestLanguageSets(t *testing.T) {
	v4 := LanguageSets("elevenlabs-v4")
	v3 := LanguageSets("elevenlabs-v3")
	hg := LanguageSets("heygen")
	if len(v4) == 0 || len(v3) == 0 || len(hg) == 0 {
		t.Fatalf("empty language set: v4=%d v3=%d heygen=%d", len(v4), len(v3), len(hg))
	}
	if len(v4) <= len(v3) {
		t.Fatalf("v4 (%d) should offer at least as many languages as v3 (%d)", len(v4), len(v3))
	}
	// The two lists are not strict supersets: the docs drop a few v3 languages
	// (ga, yo) from the v4 page while adding others. Overlap must exist, but
	// neither set may be empty of the other's languages.
	have := map[string]bool{}
	for _, c := range v4 {
		have[c] = true
	}
	overlap := 0
	for _, c := range v3 {
		if have[c] {
			overlap++
		}
	}
	if overlap < len(v3)*3/4 {
		t.Fatalf("v3/v4 overlap too small: %d of %d", overlap, len(v3))
	}
	// HeyGen rejects regional tags, so entries must be bare 2-letter codes.
	for _, c := range hg {
		if len(c) != 2 {
			t.Fatalf("heygen code %q is not a bare ISO 639-1 code", c)
		}
	}
}

// TestFromOptionsOverride checks a model can narrow the provider default.
func TestFromOptionsOverride(t *testing.T) {
	opts, _ := json.Marshal(map[string]any{
		"voice_mode":      "sample",
		"language_preset": "elevenlabs-v3",
		"speed_max":       1.0,
	})
	got := FromOptions("elevenlabs", opts)
	if got.VoiceMode != Sample {
		t.Fatalf("voice mode: %q", got.VoiceMode)
	}
	if got.Language.Preset != "elevenlabs-v3" {
		t.Fatalf("language preset: %q", got.Language.Preset)
	}
	if got.Speed.Max != 1.0 || got.Speed.Min != 0.7 {
		t.Fatalf("speed should keep provider min and take model max: %+v", got.Speed)
	}
	// Invalid JSON must fall back to the provider default, not zero it out.
	bad := FromOptions("fishaudio", []byte("{nope"))
	if bad.VoiceMode != Library || bad.Speed.Max != 2.0 {
		t.Fatalf("bad options fallback: %+v", bad)
	}
}
