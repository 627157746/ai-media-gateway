package admin

import (
	"errors"
	"net/http"
	"strings"

	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/store"
)

// voiceOut is the admin view of a voice: it includes upstream_id, which the
// client-facing /v1/audio/voices deliberately hides.
func voiceOut(v *store.Voice) map[string]any {
	return map[string]any{
		"id":          v.ID,
		"name":        v.Name,
		"provider_id": v.ProviderID,
		"upstream_id": v.UpstreamID,
		"kind":        v.Kind,
		"options":     map[string]any{},
		"enabled":     v.Enabled,
		"created_at":  v.CreatedAt,
		"updated_at":  v.UpdatedAt,
	}
}

func (h *handler) listVoices(w http.ResponseWriter, r *http.Request) {
	vs, err := h.st.ListVoices(r.Context(), strings.TrimSpace(r.URL.Query().Get("provider_id")))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]map[string]any, 0, len(vs))
	for _, v := range vs {
		out = append(out, voiceOut(v))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"voices": out})
}

type voiceIn struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ProviderID string `json:"provider_id"`
	UpstreamID string `json:"upstream_id"`
	Enabled    *bool  `json:"enabled"`
}

func (h *handler) createVoice(w http.ResponseWriter, r *http.Request) {
	var in voiceIn
	if !readJSON(w, r, &in) {
		return
	}
	v := &store.Voice{
		ID:         strings.TrimSpace(in.ID),
		Name:       strings.TrimSpace(in.Name),
		ProviderID: strings.TrimSpace(in.ProviderID),
		UpstreamID: strings.TrimSpace(in.UpstreamID),
		Enabled:    true,
	}
	if in.Enabled != nil {
		v.Enabled = *in.Enabled
	}
	if err := h.validateVoice(r, v); err != nil {
		fail(w, err)
		return
	}
	if err := h.st.CreateVoice(r.Context(), v); err != nil {
		fail(w, voiceErr(err, v))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, voiceOut(v))
}

type voicePatchIn struct {
	Name       *string `json:"name"`
	UpstreamID *string `json:"upstream_id"`
	Enabled    *bool   `json:"enabled"`
}

func (h *handler) patchVoice(w http.ResponseWriter, r *http.Request) {
	var in voicePatchIn
	if !readJSON(w, r, &in) {
		return
	}
	if in.Name != nil {
		v := strings.TrimSpace(*in.Name)
		if v == "" || len(v) > 64 {
			fail(w, bad("name 必填且不超过 64 字符"))
			return
		}
		in.Name = &v
	}
	if in.UpstreamID != nil {
		v := strings.TrimSpace(*in.UpstreamID)
		if v == "" || len(v) > 256 {
			fail(w, bad("upstream_id 必填且不超过 256 字符"))
			return
		}
		in.UpstreamID = &v
	}
	id := r.PathValue("id")
	if err := h.st.UpdateVoice(r.Context(), id, in.Name, in.UpstreamID, in.Enabled); err != nil {
		fail(w, voiceErr(err, nil))
		return
	}
	v, err := h.st.GetVoice(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, voiceOut(v))
}

func (h *handler) deleteVoice(w http.ResponseWriter, r *http.Request) {
	if err := h.st.DeleteVoice(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// voiceErr turns the generic store conflict into an actionable message: the
// only conflict a voice can hit is the (provider_id, upstream_id) unique index.
func voiceErr(err error, v *store.Voice) error {
	if !errors.Is(err, store.ErrConflict) {
		return err
	}
	if v != nil {
		return httpx.Conflict("该平台下已经存在上游音色 %s，请勿重复添加", v.UpstreamID)
	}
	return httpx.Conflict("该平台下已经存在相同的上游音色 ID")
}

// validateVoice checks the invariants that must fail as 400 rather than a raw
// driver/FK error (500): field shape, id format and provider existence.
func (h *handler) validateVoice(r *http.Request, v *store.Voice) error {
	if v.ID != "" && !validID(v.ID) {
		return bad("id 只能包含小写字母、数字和连字符")
	}
	if v.Name == "" || len(v.Name) > 64 {
		return bad("name 必填且不超过 64 字符")
	}
	if v.ProviderID == "" {
		return bad("provider_id 必填")
	}
	if v.UpstreamID == "" || len(v.UpstreamID) > 256 {
		return bad("upstream_id 必填且不超过 256 字符")
	}
	if _, err := h.st.GetProvider(r.Context(), v.ProviderID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return bad("平台 %s 不存在", v.ProviderID)
		}
		return err
	}
	return nil
}

// upstreamVoices lists the provider's own voice library for the 从上游导入
// picker. Adapters without a voice library (fal) answer 400 with an explicit
// hint instead of an empty list, so the UI can tell the user to type the id.
func (h *handler) upstreamVoices(w http.ResponseWriter, r *http.Request) {
	p, err := h.st.GetProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	adapter, ok := provider.Get(p.Type)
	if !ok {
		fail(w, bad("unknown provider type %q", p.Type))
		return
	}
	lister, ok := adapter.(provider.VoiceLister)
	if !ok {
		fail(w, bad("平台 %s 不支持自动拉取音色，请手动填写上游音色 ID", p.Type))
		return
	}
	call, key, err := h.upstreamCall(r.Context(), p, false)
	if err != nil {
		fail(w, err)
		return
	}
	voices, err := lister.ListVoices(r.Context(), call)
	if err != nil {
		fail(w, scrubKey(err, key))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": voices})
}
