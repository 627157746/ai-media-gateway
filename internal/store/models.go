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
	ID         *string
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
	rename := p.ID != nil && *p.ID != id
	if rename {
		add("id", *p.ID)
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
	q := `UPDATE models SET ` + strings.Join(sets, ",") + ` WHERE id=?`
	if !rename {
		res, err := s.db.ExecContext(ctx, q, args...)
		return oneRow(res, err)
	}
	// video_tasks.model_id holds the id by value (no FK), so the rename and
	// the task rewrite must commit or roll back together.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM models WHERE id=?`, *p.ID).Scan(&one)
	if err == nil {
		tx.Rollback()
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		tx.Rollback()
		return err
	}
	res, err := tx.ExecContext(ctx, q, args...)
	if err == nil {
		err = oneRow(res, nil)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE video_tasks SET model_id=? WHERE model_id=?`, *p.ID, id)
	}
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteModel(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE id=?`, id)
	return oneRow(res, err)
}
