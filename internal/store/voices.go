package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Voice is one entry of the 音色库: a friendly local name pointing at a
// vendor voice id, scoped to the provider that owns it.
type Voice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ProviderID string `json:"provider_id"`
	// UpstreamID is a vendor voice id for KindUpstream, or a local
	// reference-audio path for KindSample.
	UpstreamID string    `json:"upstream_id"`
	Kind       string    `json:"kind"`
	Options    []byte    `json:"options,omitempty"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Voice kinds.
const (
	// KindUpstream points at a durable vendor voice (ElevenLabs voice_id,
	// Fish Audio reference_id).
	KindUpstream = "upstream"
	// KindSample points at a local reference-audio file used for zero-shot
	// cloning on every synthesis request (Fish Audio references[]).
	KindSample = "sample"
)

const voiceCols = `id,name,provider_id,upstream_id,kind,options,enabled,created_at,updated_at`

func scanVoice(row interface{ Scan(...any) error }) (*Voice, error) {
	v := &Voice{}
	var opts []byte
	var created, updated int64
	err := row.Scan(&v.ID, &v.Name, &v.ProviderID, &v.UpstreamID, &v.Kind, &opts, &v.Enabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.Options = []byte(optsOrEmpty(opts))
	v.CreatedAt, v.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	return v, nil
}

func (s *Store) GetVoice(ctx context.Context, id string) (*Voice, error) {
	return scanVoice(s.db.QueryRowContext(ctx,
		`SELECT `+voiceCols+` FROM voices WHERE id=?`, id))
}

// ListVoices returns voices ordered by name then id; providerID="" means all.
func (s *Store) ListVoices(ctx context.Context, providerID string) ([]*Voice, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if providerID == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT `+voiceCols+` FROM voices ORDER BY name, id`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+voiceCols+` FROM voices WHERE provider_id=? ORDER BY name, id`, providerID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Voice{}
	for rows.Next() {
		v, err := scanVoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// FindVoice resolves a client-supplied voice key to an enabled voice of the
// given provider. Only the id is matched: names are editable and need not be
// unique, so they must not be used as request keys.
func (s *Store) FindVoice(ctx context.Context, providerID, id string) (*Voice, error) {
	if providerID == "" || id == "" {
		return nil, nil
	}
	v, err := scanVoice(s.db.QueryRowContext(ctx,
		`SELECT `+voiceCols+` FROM voices WHERE provider_id=? AND id=? AND enabled=1`, providerID, id))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return v, err
}

func (s *Store) CreateVoice(ctx context.Context, v *Voice) error {
	if v.ID == "" {
		v.ID = "vc_" + RandHex(6)
	}
	now := nowMS()
	if v.Kind == "" {
		v.Kind = KindUpstream
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO voices(`+voiceCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
		v.ID, v.Name, v.ProviderID, v.UpstreamID, v.Kind, optsOrEmpty(v.Options), v.Enabled, now, now)
	if err != nil {
		if isVoiceUpstreamConflict(err) {
			return ErrConflict
		}
		return err
	}
	v.CreatedAt, v.UpdatedAt = time.UnixMilli(now), time.UnixMilli(now)
	return nil
}

// UpdateVoice patches name/upstream_id/enabled; nil = keep.
func (s *Store) UpdateVoice(ctx context.Context, id string, name, upstreamID *string, enabled *bool) error {
	sets := []string{"updated_at=?"}
	args := []any{nowMS()}
	if name != nil {
		sets = append(sets, "name=?")
		args = append(args, *name)
	}
	if upstreamID != nil {
		sets = append(sets, "upstream_id=?")
		args = append(args, *upstreamID)
	}
	if enabled != nil {
		sets = append(sets, "enabled=?")
		args = append(args, *enabled)
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE voices SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	if err != nil {
		if isVoiceUpstreamConflict(err) {
			return ErrConflict
		}
		return err
	}
	return oneRow(res, nil)
}

func (s *Store) DeleteVoice(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM voices WHERE id=?`, id)
	return oneRow(res, err)
}

// isVoiceUpstreamConflict reports whether err is the (provider_id,
// upstream_id) unique-index violation, so callers see ErrConflict instead of
// a raw driver error. Matched on message text to avoid importing the sqlite
// driver outside store.
func isVoiceUpstreamConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") && strings.Contains(msg, "voices.")
}
