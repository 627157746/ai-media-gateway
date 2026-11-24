package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ai-media-gateway/internal/media"
)

type Model struct {
	ID         string          `json:"id"`
	ProviderID string          `json:"provider_id"`
	Kind       media.Kind      `json:"kind"`
	Upstream   string          `json:"upstream"`
	Options    json.RawMessage `json:"options"`
	Enabled    bool            `json:"enabled"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

const modelCols = `id,provider_id,kind,upstream,options,enabled,created_at,updated_at`

func scanModel(row interface{ Scan(...any) error }) (*Model, error) {
	m := &Model{}
	var kind string
	var opts []byte
	var created, updated int64
	err := row.Scan(&m.ID, &m.ProviderID, &kind, &m.Upstream, &opts, &m.Enabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Kind = media.Kind(kind)
	m.Options = opts
	m.CreatedAt, m.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	return m, nil
}

func (s *Store) GetModel(ctx context.Context, id string) (*Model, error) {
	return scanModel(s.db.QueryRowContext(ctx,
		`SELECT `+modelCols+` FROM models WHERE id=?`, id))
}

// ListModels returns models for providerID, or all when providerID is "".
func (s *Store) ListModels(ctx context.Context, providerID string) ([]*Model, error) {
	q := `SELECT ` + modelCols + ` FROM models`
	var rows *sql.Rows
	var err error
	if providerID == "" {
		rows, err = s.db.QueryContext(ctx, q+` ORDER BY id`)
	} else {
		rows, err = s.db.QueryContext(ctx, q+` WHERE provider_id=? ORDER BY id`, providerID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) CreateModel(ctx context.Context, m *Model) error {
	if _, err := s.GetModel(ctx, m.ID); err == nil {
		return ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO models(`+modelCols+`) VALUES(?,?,?,?,?,?,?,?)`,
		m.ID, m.ProviderID, string(m.Kind), m.Upstream, optsOrEmpty(m.Options), m.Enabled, now, now)
	if err != nil {
		return err
	}
	m.CreatedAt, m.UpdatedAt = time.UnixMilli(now), time.UnixMilli(now)
	return nil
}

type ModelPatch struct {
	ProviderID *string
	Kind       *media.Kind
	Upstream   *string
	Options    *json.RawMessage
	Enabled    *bool
}

func (s *Store) UpdateModel(ctx context.Context, id string, p ModelPatch) error {
	sets := []string{"updated_at=?"}
	args := []any{nowMS()}
	add := func(col string, v any) {
		sets = append(sets, col+"=?")
		args = append(args, v)
	}
	if p.ProviderID != nil {
		add("provider_id", *p.ProviderID)
	}
	if p.Kind != nil {
		add("kind", string(*p.Kind))
	}
	if p.Upstream != nil {
		add("upstream", *p.Upstream)
	}
	if p.Options != nil {
		add("options", optsOrEmpty(*p.Options))
	}
	if p.Enabled != nil {
		add("enabled", *p.Enabled)
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE models SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return oneRow(res, err)
}

func (s *Store) DeleteModel(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE id=?`, id)
	return oneRow(res, err)
}
