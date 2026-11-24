package openai

import (
	"os"
	"path/filepath"
	"strings"

	"ai-media-gateway/internal/media"
)

// voiceSampleDir is where reference-audio samples live for zero-shot cloning.
// It sits next to the SQLite file so one backup captures both.
func voiceSampleDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "voices")
}

// readVoiceSample loads a zero-shot reference sample. The stored path is
// treated as data (never as a caller-supplied path): it is rejected unless it
// stays inside voiceSampleDir, so a tampered row cannot turn this into an
// arbitrary file read.
func (h *handler) readVoiceSample(rel string) (*media.Input, error) {
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
	relCheck, err := filepath.Rel(absDir, abs)
	if err != nil || strings.HasPrefix(relCheck, "..") || filepath.IsAbs(relCheck) {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return &media.Input{Name: filepath.Base(abs), Data: data, MIME: sampleMIME(abs)}, nil
}

// sampleMIME maps a sample extension to a content type; Fish Audio sniffs the
// bytes, but the multipart upload and logs are easier to read with a real type.
func sampleMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	default:
		return "audio/mpeg"
	}
}
