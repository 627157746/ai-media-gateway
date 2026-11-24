package admin

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/store"
)

// maxSampleBytes caps one uploaded reference sample. Voice promotion usually
// wants 10s–3min of clean speech; 25 MB covers lossless WAV at that length.
const maxSampleBytes = 25 << 20

// voiceSampleDir mirrors openai.voiceSampleDir: one directory of reference
// samples next to the SQLite file, shared by cloning and synthesis.
func voiceSampleDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "voices")
}

// cloneVoice turns an uploaded recording into a 音色库 entry.
//
// Two vendor shapes exist and the difference is not cosmetic:
//   - ElevenLabs creates a durable voice (POST /v1/voices/add) and returns an
//     id we store as KindUpstream.
//   - Fish Audio clones zero-shot: nothing is created upstream, the sample is
//     replayed inside every synthesis request. We therefore store the file and
//     record KindSample.
func (h *handler) cloneVoice(w http.ResponseWriter, r *http.Request) {
	if h.dbPath == "" {
		fail(w, bad("音色克隆未启用（缺少数据库路径配置）"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSampleBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		fail(w, bad("无法解析上传内容：%v", err))
		return
	}
	providerID := strings.TrimSpace(r.FormValue("provider_id"))
	name := strings.TrimSpace(r.FormValue("name"))
	if providerID == "" || name == "" {
		fail(w, bad("provider_id 和 name 必填"))
		return
	}
	if len(name) > 64 {
		fail(w, bad("name 不超过 64 字符"))
		return
	}
	samples, err := readSamples(r)
	if err != nil {
		fail(w, bad("%v", err))
		return
	}
	if len(samples) == 0 {
		fail(w, bad("请至少上传一个音频样本"))
		return
	}

	p, err := h.st.GetProvider(r.Context(), providerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, bad("平台 %s 不存在", providerID))
			return
		}
		fail(w, err)
		return
	}
	adapter, ok := provider.Get(p.Type)
	if !ok {
		fail(w, bad("unknown provider type %q", p.Type))
		return
	}

	switch {
	case implementsVoiceCloner(adapter):
		h.cloneDurable(w, r, p, adapter.(provider.VoiceCloner), name, samples)
	case p.Type == "fishaudio" || p.Type == "mimo":
		h.cloneZeroShot(w, r, p, name, samples)
	default:
		fail(w, bad("平台 %s 不支持音色克隆", p.Type))
	}
}

func implementsVoiceCloner(a provider.Adapter) bool {
	_, ok := a.(provider.VoiceCloner)
	return ok
}

// cloneDurable asks the vendor to create a voice and stores its id.
func (h *handler) cloneDurable(
	w http.ResponseWriter, r *http.Request,
	p *store.Provider, cloner provider.VoiceCloner,
	name string, samples [][]byte,
) {
	call, key, err := h.upstreamCall(r.Context(), p, false)
	if err != nil {
		fail(w, err)
		return
	}
	res, err := cloner.CloneVoice(r.Context(), call, provider.CloneRequest{
		Name:  name,
		Files: samples,
	})
	if err != nil {
		fail(w, scrubKey(err, key))
		return
	}
	v := &store.Voice{
		Name:       name,
		ProviderID: p.ID,
		UpstreamID: res.VoiceID,
		Kind:       store.KindUpstream,
		Enabled:    !res.RequiresVerification,
	}
	if err := h.st.CreateVoice(r.Context(), v); err != nil {
		// The vendor voice exists but we could not record it; surface the id so
		// the user can add it by hand instead of cloning a second time.
		if errors.Is(err, store.ErrConflict) {
			fail(w, httpx.Conflict("上游音色 %s 已在库中（克隆已完成，无需重复创建）", res.VoiceID))
			return
		}
		fail(w, fmt.Errorf("克隆已成功（voice_id=%s），但写入音色库失败: %w", res.VoiceID, err))
		return
	}
	out := voiceOut(v)
	out["requires_verification"] = res.RequiresVerification
	if res.RequiresVerification {
		out["notice"] = "上游要求该音色先完成验证，已存为停用状态；验证通过后手动启用即可"
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

// cloneZeroShot stores the sample locally; Fish Audio replays it per request.
func (h *handler) cloneZeroShot(
	w http.ResponseWriter, r *http.Request,
	p *store.Provider, name string, samples [][]byte,
) {
	src := samples[0]
	ext := sampleExt(src)
	if err := os.MkdirAll(voiceSampleDir(h.dbPath), 0o755); err != nil {
		fail(w, err)
		return
	}
	v := &store.Voice{
		Name:       name,
		ProviderID: p.ID,
		Kind:       store.KindSample,
		Enabled:    true,
	}
	if v.ID == "" {
		v.ID = "vc_" + store.RandHex(6)
	}
	rel := filepath.ToSlash(filepath.Join(v.ID, "sample."+ext))
	dir := voiceSampleDir(h.dbPath)
	if err := os.MkdirAll(filepath.Join(dir, v.ID), 0o755); err != nil {
		fail(w, err)
		return
	}
	full := filepath.Join(dir, v.ID, "sample."+ext)
	if err := os.WriteFile(full, src, 0o644); err != nil {
		fail(w, err)
		return
	}
	v.UpstreamID = rel
	if err := h.st.CreateVoice(r.Context(), v); err != nil {
		// Do not leave an orphan sample behind.
		_ = os.RemoveAll(filepath.Join(dir, v.ID))
		if errors.Is(err, store.ErrConflict) {
			fail(w, httpx.Conflict("同一参考音频已存在"))
			return
		}
		fail(w, err)
		return
	}
	out := voiceOut(v)
	switch p.Type {
	case "fishaudio":
		out["notice"] = "Fish Audio 使用零样本克隆：每次合成都会上传这段参考音频，无需等待训练"
	case "mimo":
		out["notice"] = "MiMo 使用零样本克隆：样本存本地，合成时以 data URL 上传，无需等待训练"
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

// readVoiceSample loads a stored reference sample, rejecting any path that
// escapes the voices directory. The row is data, not a trusted path: a tampered
// upstream_id must not turn synthesis into an arbitrary file read.
func (h *handler) readVoiceSample(rel string) ([]byte, error) {
	dir := voiceSampleDir(h.dbPath)
	full := rel
	if !filepath.IsAbs(full) {
		full = filepath.Join(dir, filepath.Clean(strings.TrimPrefix(rel, "/")))
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(full)
	if err != nil {
		return nil, err
	}
	if relCheck, err := filepath.Rel(absDir, abs); err != nil || strings.HasPrefix(relCheck, "..") || filepath.IsAbs(relCheck) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(abs)
}

// readSamples collects uploaded audio, tolerating the common field names.
func readSamples(r *http.Request) ([][]byte, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	var out [][]byte
	for _, field := range []string{"files", "files[]", "file", "sample", "audio"} {
		for _, fh := range r.MultipartForm.File[field] {
			if fh.Size > maxSampleBytes {
				return nil, fmt.Errorf("样本 %s 超过 %d MB", fh.Filename, maxSampleBytes>>20)
			}
			f, err := fh.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				return nil, err
			}
			if len(data) == 0 {
				continue
			}
			out = append(out, data)
		}
	}
	return out, nil
}

// sampleExt derives a filename extension from the audio signature.
func sampleExt(data []byte) string {
	switch {
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "wav"
	case len(data) >= 3 && string(data[0:3]) == "ID3":
		return "mp3"
	case len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0:
		return "mp3"
	case len(data) >= 4 && string(data[0:4]) == "OggS":
		return "ogg"
	case len(data) >= 4 && string(data[0:4]) == "fLaC":
		return "flac"
	default:
		return "mp3"
	}
}
