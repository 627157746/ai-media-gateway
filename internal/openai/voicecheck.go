package openai

import (
	"context"
	"encoding/json"
	"strings"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/capability"
)

// openAIVoices are OpenAI's built-in voice names. Clients written against the
// OpenAI API (Infinite Canvas defaults to "alloy") send them to every model, but
// no other vendor knows them: forwarded as-is they become an opaque upstream
// failure such as ElevenLabs' /v1/text-to-speech/alloy 404.
var openAIVoices = map[string]bool{
	"alloy": true, "ash": true, "ballad": true, "cedar": true, "coral": true,
	"echo": true, "fable": true, "marin": true, "nova": true, "onyx": true,
	"sage": true, "shimmer": true, "verse": true,
}

// checkVoice rejects a voice the model cannot use, before any upstream call.
//
// It runs after the 音色库 lookup, so `voice` is the value that would reach the
// adapter. The rules follow the model's voice_mode:
//   - sample: a stored sample (or a data:audio URL) is mandatory.
//   - prompt: a free-text description is mandatory; an OpenAI name is not one.
//   - preset / library: an empty voice keeps the adapter's own default
//     (mimo_default, the ElevenLabs account's first voice, Fish's default,
//     HeyGen's default_voice); an OpenAI name is rejected; a preset model with
//     a declared list only accepts names from it.
//
// A value mapped by an `options.voices` alias (model, provider or any account of
// the provider) is always allowed: the adapter rewrites it to a vendor id.
func (h *handler) checkVoice(ctx context.Context, res accounts.Resolved, spec capability.Spec, voice string, resolved, hasSample bool) error {
	model := res.Model.ID
	switch spec.VoiceMode {
	case capability.None:
		return nil
	case capability.Sample:
		if hasSample || strings.HasPrefix(voice, "data:audio/") {
			return nil
		}
		if voice == "" {
			return bad("模型 %s 是声音克隆模型：voice 需要填音色库里的样本音色 id（先在音色库上传参考音频）", model)
		}
		return bad("模型 %s 是声音克隆模型：%q 不是音色库里的样本音色，请先在音色库上传参考音频，再传它的 id", model, voice)
	}
	if resolved {
		return nil
	}
	if voice != "" && h.voiceAliased(ctx, res, voice) {
		return nil
	}
	isOpenAI := openAIVoices[strings.ToLower(voice)]
	if spec.VoiceMode == capability.Prompt {
		if voice == "" {
			return bad("模型 %s 是音色设计模型：voice 需要填音色描述（例如「温柔的年轻女声」）", model)
		}
		if isOpenAI {
			return bad("模型 %s 是音色设计模型：%q 是 OpenAI 的音色名，不是音色描述；请在 voice 里填音色描述", model, voice)
		}
		return nil
	}
	if voice == "" {
		return nil
	}
	presets := spec.VoiceMode == capability.Preset && len(spec.VoicePresets) > 0
	if isOpenAI {
		hint := "请传音色库里的音色 id，或厂商自己的音色 ID，或在模型 options.voices 里给它配置映射"
		if presets {
			hint = "可选预设：" + strings.Join(spec.VoicePresets, "、") + "；不传 voice 则用默认音色"
		}
		return bad("模型 %s 不认识 OpenAI 音色 %q：%s", model, voice, hint)
	}
	if presets && !containsVoice(spec.VoicePresets, voice) {
		return bad("模型 %s 没有预设音色 %q，可选：%s；不传 voice 则用默认音色", model, voice, strings.Join(spec.VoicePresets, "、"))
	}
	return nil
}

// voiceAliased reports whether some options.voices map would rewrite voice,
// mirroring the places provider.ResolveVoice looks at.
func (h *handler) voiceAliased(ctx context.Context, res accounts.Resolved, voice string) bool {
	if aliasIn(res.Model.Options, voice) || aliasIn(res.Provider.Options, voice) {
		return true
	}
	accs, err := h.st.ListAccounts(ctx, res.Provider.ID)
	if err != nil {
		return false
	}
	for _, a := range accs {
		if aliasIn(a.Options, voice) {
			return true
		}
	}
	return false
}

func aliasIn(opts []byte, voice string) bool {
	if len(opts) == 0 {
		return false
	}
	var o struct {
		Voices map[string]string `json:"voices"`
	}
	if json.Unmarshal(opts, &o) != nil {
		return false
	}
	_, ok := o.Voices[voice]
	return ok
}

func containsVoice(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
