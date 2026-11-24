package openai

import (
	"errors"
	"net/http"
	"sort"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/capability"
	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/store"
)

// listVoices serves the 音色库 to clients (Infinite Canvas pulls it to build
// its voice picker). upstream_id is deliberately omitted: clients send our
// voice id back and the mapping to the vendor id stays server-side.
//
// With ?model=, the response also carries voice_mode: what that model actually
// accepts in the `voice` field. Voices are per provider, but not every model of
// a provider uses them — MiMo's plain model wants a preset name and its
// design model wants a free-text description, so returning the library there
// would hand the client ids that can only fail.
//
// The filter is best-effort: an unresolvable model yields the unfiltered list
// (mode library) rather than an empty one, so a half-configured client still
// sees its options.
func (h *handler) listVoices(w http.ResponseWriter, r *http.Request) {
	queued := r.URL.Query()
	providerID := ""
	var model *store.Model
	// Unresolvable model → library mode with no provider filter: a
	// half-configured client should still see its options rather than nothing.
	spec := capability.For("")
	if id := queued.Get("model"); id != "" {
		m, err := h.st.GetModel(r.Context(), id)
		switch {
		case err == nil:
			model = m
			spec = capability.FromOptions(m.ProviderID, m.Options)
		case errors.Is(err, store.ErrNotFound):
			// Passthrough ids ("provider/model") resolve through accounts.Resolve
			// instead of the models table.
			if res, rerr := accounts.Resolve(r.Context(), h.st, id, media.KindSpeech); rerr == nil {
				providerID = res.Provider.ID
				spec = capability.FromOptions(res.Provider.ID, res.Model.Options)
			}
		case err != nil:
			httpx.Fail(w, r, err)
			return
		}
		if model != nil {
			providerID = model.ProviderID
			if p, perr := h.st.GetProvider(r.Context(), providerID); perr == nil {
				spec = capability.FromOptions(p.Type, model.Options)
			}
		}
	}

	// Modes that do not consume a library entry return no entries at all; the
	// client renders a preset picker / prompt box / nothing based on voice_mode.
	mode := spec.VoiceMode
	data := []map[string]any{}
	if mode == capability.Library || mode == capability.Sample {
		vs, err := h.st.ListVoices(r.Context(), providerID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		for _, v := range vs {
			if !v.Enabled {
				continue
			}
			// A zero-shot sample voice only makes sense for a model that embeds
			// samples; a durable vendor id only for one that resolves a lookup.
			if mode == capability.Sample && v.Kind != store.KindSample {
				continue
			}
			if mode == capability.Library && v.Kind == store.KindSample {
				continue
			}
			data = append(data, map[string]any{
				"id":          v.ID,
				"name":        v.Name,
				"provider_id": v.ProviderID,
				"object":      "voice",
			})
		}
	}
	sort.SliceStable(data, func(i, j int) bool {
		ni, _ := data[i]["name"].(string)
		nj, _ := data[j]["name"].(string)
		if ni != nj {
			return ni < nj
		}
		ii, _ := data[i]["id"].(string)
		ij, _ := data[j]["id"].(string)
		return ii < ij
	})
	out := map[string]any{
		"object":     "list",
		"data":       data,
		"voice_mode": string(mode),
		// capabilities is the full declaration so clients render controls from
		// one contract instead of hardcoding per-provider rules.
		"capabilities": spec,
	}
	if spec.Language.Supported && spec.Language.Preset != "" {
		if codes := capability.LanguageSets(spec.Language.Preset); len(codes) > 0 {
			out["languages"] = codes
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
