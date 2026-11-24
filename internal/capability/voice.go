// Package voicecap is the single source of truth for what each speech provider
// and model accepts.
//
// Before this existed, the same facts lived in four places: each adapter
// hardcoded its own speed clamp, voices.go inferred what the UI should render,
// and the playground re-declared language lists and preset names in
// JavaScript. Adding a provider meant touching all of them and they drifted.
//
// Now every capability is declared once in the registry below. Adapters ask it
// to clamp a value, the API serves it to clients, and the admin UI renders
// controls from it. Adding a provider is one registry entry.
package capability

import (
	"encoding/json"
	"strings"
)

// VoiceMode says how the `voice` field is interpreted.
type VoiceMode string

const (
	// Preset: the vendor's own named voices (HeyGen voice_id, MiMo 冰糖).
	Preset VoiceMode = "preset"
	// Library: a gateway 音色库 entry, resolved to a vendor id.
	Library VoiceMode = "library"
	// Sample: a reference recording, replayed per request (zero-shot).
	Sample VoiceMode = "sample"
	// Prompt: free-text timbre description (voice design).
	Prompt VoiceMode = "prompt"
	// None: the model ignores voice.
	None VoiceMode = "none"
)

// SpeedMode says how a rate change reaches the vendor.
type SpeedMode string

const (
	// SpeedField: a numeric field in the request body.
	SpeedField SpeedMode = "field"
	// SpeedPrompt: MiMo has no rate field; rate is natural language in the user
	// turn, so the number is turned into phrasing.
	SpeedPrompt SpeedMode = "prompt"
	// SpeedNone: unsupported, must not be sent.
	SpeedNone SpeedMode = "none"
)

// Spec describes one model's speech capabilities.
type Spec struct {
	VoiceMode     VoiceMode   `json:"voice_mode"`
	VoicePresets  []string    `json:"voice_presets,omitempty"`
	DefaultPreset string      `json:"default_preset,omitempty"`
	Formats       []string    `json:"formats"`
	Language      LanguageCap `json:"language"`
	Speed         SpeedCap    `json:"speed"`
	// InstructionsUses names where `instructions` ends up, so a client can tell
	// whether the field does anything: "" (ignored), "prompt" (user message).
	InstructionsUses string `json:"instructions_uses,omitempty"`
	// VoiceRequired means the vendor rejects a request without a voice.
	VoiceRequired bool `json:"voice_required,omitempty"`
}

// LanguageCap describes language acceptance.
type LanguageCap struct {
	Supported bool `json:"supported"`
	// Preset is a named language set ("elevenlabs-v4", "heygen") so a client can
	// cache the list instead of receiving hundreds of codes inline.
	Preset string `json:"preset,omitempty"`
	// Inline carries the codes when the set is small or provider-specific.
	Inline []string `json:"inline,omitempty"`
	// RejectsRegional means "en-US" is invalid; only "en" is accepted.
	RejectsRegional bool `json:"rejects_regional,omitempty"`
	// Note explains provider quirks worth surfacing in the UI.
	Note string `json:"note,omitempty"`
}

// SpeedCap describes speed acceptance.
type SpeedCap struct {
	Mode SpeedMode `json:"mode"`
	Min  float64   `json:"min,omitempty"`
	Max  float64   `json:"max,omitempty"`
}

// Clamp bounds v to the declared range. A non-positive input means "unset" and
// returns 0 so callers can skip the field entirely.
func (c SpeedCap) Clamp(v float64) float64 {
	// Only a numeric field can be clamped. A prompt-mode provider (MiMo) has no
	// rate field to bound, and SpeedNone must never emit a number at all.
	if c.Mode != SpeedField || v <= 0 {
		return 0
	}
	if c.Min > 0 && v < c.Min {
		return c.Min
	}
	if c.Max > 0 && v > c.Max {
		return c.Max
	}
	return v
}

// SpeedPhrase renders a speed as natural language for SpeedPrompt providers.
// Values near 1.0 are "no preference" and yield "".
func (c SpeedCap) SpeedPhrase(v float64) string {
	if c.Mode != SpeedPrompt || v <= 0 {
		return ""
	}
	switch {
	case v < 0.85:
		return "语速放慢"
	case v <= 1.15:
		return ""
	case v < 1.5:
		return "语速稍快"
	default:
		return "语速明显加快"
	}
}

// ---- language sets (declared once, referenced by preset name) ----

// elevenV4Languages: ElevenLabs v4 / v4 Turbo, 91 codes (docs: "90+").
var elevenV4Languages = strings.Fields(
	"af am ar hy as ast az be bn bs bg my yue ca ceb ny hr cs da nl en et fil fi fr ff gl ka de el gu ha he hi hu is id it ja jv kam kn kk ko ky lo lv ln lt lg lb mk ms ml mt zh mi mr mn ne no oc or ps fa pl pt pa ro ru sr sn sd sk sl so ckb es sw sv tg ta te th tr uk ur uz vi cy wo zu")

// elevenV3Languages: ElevenLabs v3, 85 codes (docs: "70+").
var elevenV3Languages = strings.Fields(
	"af ar hy as ast az be bn bs bg my yue ca ceb ny hr cs da nl en et fil fi fr gl ka de el gu ha he hi hu is id ga it ja jv kn kk ky ko lv ln lt lb mk ms ml mt zh mi mr mn ne no oc or ps fa pl pt pa ro ru sr sd sk sl so es sw sv tg ta te th tr uk ur uz vi cy yo")

// elevenMultilingualV2Languages: 29 codes.
var elevenMultilingualV2Languages = strings.Fields(
	"en ja zh de hi fr ko pt it es id nl tr fil pl sv bg ro ar cs el fi hr ms sk da ta uk ru")

// fishLanguages: base codes from Fish's documented normalization-tag table.
// The model covers 83 languages but the docs publish normalization support
// rather than an exhaustive list, so this is the set that actually affects
// readings (which is all `language` does for Fish).
var fishLanguages = strings.Fields(
	"en zh ja ko es fr de it pt ru ar tr tl fil sv ms nl pl cs da el fi hr hu id no ro sk sl ta uk vi bg")

// heygenLanguages: lowercase ISO 639-1 only; regional tags are rejected.
var heygenLanguages = strings.Fields(
	"ar be bg ca cs da de el en es fa fi fr he hi hr hu id it ja ko mk ms nl pl pt ro ru sk sl sr sv ta th tl tr uk vi zh")

// Providers is the registry: provider type → default spec. A model's
// options may override individual fields (see FromOptions).
//
// Keep this table authoritative: adapters clamp with it and clients render
// from it, so a value here is the only place a range needs to change.
var Providers = map[string]Spec{
	"elevenlabs": {
		VoiceMode: Library,
		Formats:   []string{"mp3", "opus", "pcm", "wav"},
		Speed:     SpeedCap{Mode: SpeedField, Min: 0.7, Max: 1.2},
		Language: LanguageCap{
			Supported: true,
			Preset:    "elevenlabs-v4",
			Note:      "默认 eleven_v4；multilingual_v2 会拒绝 language_code",
		},
	},
	"fishaudio": {
		VoiceMode: Library,
		Formats:   []string{"mp3", "wav", "pcm", "opus"},
		Speed:     SpeedCap{Mode: SpeedField, Min: 0.5, Max: 2.0},
		Language: LanguageCap{
			Supported: true,
			Preset:    "fishaudio",
			Note:      "接受 BCP-47（zh-CN、es-MX）；只影响数字/日期读法，不改变音色",
		},
	},
	"heygen": {
		VoiceMode:     Preset,
		VoiceRequired: true,
		Formats:       []string{"mp3", "pcm"},
		Speed:         SpeedCap{Mode: SpeedField, Min: 0.75, Max: 1.25},
		Language: LanguageCap{
			Supported:       true,
			Preset:          "heygen",
			RejectsRegional: true,
			Note:            "只接受小写 ISO 639-1；en-US 这类地区标签会报错",
		},
	},
	"mimo": {
		VoiceMode:        Preset,
		VoicePresets:     []string{"mimo_default", "冰糖", "茉莉", "苏打", "白桦", "Mia", "Chloe", "Milo", "Dean"},
		DefaultPreset:    "mimo_default",
		Formats:          []string{"mp3", "wav", "pcm"},
		Speed:            SpeedCap{Mode: SpeedPrompt},
		InstructionsUses: "prompt",
		Language:         LanguageCap{Supported: false, Note: "MiMo 没有 language 参数"},
	},
}

// LanguageSets resolves a preset name to its codes.
func LanguageSets(name string) []string {
	switch name {
	case "elevenlabs-v4":
		return elevenV4Languages
	case "elevenlabs-v3":
		return elevenV3Languages
	case "elevenlabs-multilingual-v2":
		return elevenMultilingualV2Languages
	case "fishaudio":
		return fishLanguages
	case "heygen":
		return heygenLanguages
	default:
		return nil
	}
}

// For returns the spec for a provider type, zero-valued and "unsupported" when
// the type is unknown so callers never panic on a new adapter.
func For(providerType string) Spec {
	if s, ok := Providers[providerType]; ok {
		return s
	}
	return Spec{
		VoiceMode: Library,
		Formats:   []string{"mp3"},
		Speed:     SpeedCap{Mode: SpeedNone},
		Language:  LanguageCap{Supported: false},
	}
}

// FromOptions applies a model's `options` overrides on top of the provider
// default. Only fields the model actually declares are replaced, so a partial
// override keeps the rest of the provider behaviour.
func FromOptions(providerType string, opts []byte) Spec {
	spec := For(providerType)
	if len(opts) == 0 {
		return spec
	}
	var o struct {
		VoiceMode        *VoiceMode `json:"voice_mode"`
		VoicePresets     []string   `json:"voice_presets"`
		VoiceDefault     *string    `json:"voice_default"`
		LanguagePreset   *string    `json:"language_preset"`
		LanguageSupports *bool      `json:"language_supported"`
		SpeedMode        *SpeedMode `json:"speed_mode"`
		SpeedMin         *float64   `json:"speed_min"`
		SpeedMax         *float64   `json:"speed_max"`
		Formats          []string   `json:"formats"`
	}
	if json.Unmarshal(opts, &o) != nil {
		return spec
	}
	if o.VoiceMode != nil {
		spec.VoiceMode = *o.VoiceMode
	}
	if len(o.VoicePresets) > 0 {
		spec.VoicePresets = o.VoicePresets
	}
	if o.VoiceDefault != nil {
		spec.DefaultPreset = *o.VoiceDefault
	}
	if o.LanguagePreset != nil {
		spec.Language.Preset = *o.LanguagePreset
		spec.Language.Supported = spec.Language.Preset != ""
	}
	if o.LanguageSupports != nil {
		spec.Language.Supported = *o.LanguageSupports
	}
	if o.SpeedMode != nil {
		spec.Speed.Mode = *o.SpeedMode
	}
	if o.SpeedMin != nil {
		spec.Speed.Min = *o.SpeedMin
	}
	if o.SpeedMax != nil {
		spec.Speed.Max = *o.SpeedMax
	}
	if len(o.Formats) > 0 {
		spec.Formats = o.Formats
	}
	return spec
}
