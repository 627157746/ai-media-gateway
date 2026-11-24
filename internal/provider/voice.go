package provider

import "encoding/json"

// ResolveVoice maps a friendly voice name to a vendor voice id via the
// "voices" / "default_voice" fields of account → model → provider options
// (first hit wins). Unknown or empty input is returned unchanged.
func ResolveVoice(c Call, voice string) string {
	for _, opts := range []json.RawMessage{c.Account.Options, c.Model.Options, c.Provider.Options} {
		if len(opts) == 0 {
			continue
		}
		var o struct {
			Voices       map[string]string `json:"voices"`
			DefaultVoice string            `json:"default_voice"`
		}
		if json.Unmarshal(opts, &o) != nil {
			continue
		}
		if voice == "" {
			if o.DefaultVoice != "" {
				return o.DefaultVoice
			}
		} else if v, ok := o.Voices[voice]; ok {
			return v
		}
	}
	return voice
}
