package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Provider struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	BaseURL   string          `json:"base_url"`
	ProxyURL  string          `json:"proxy_url"`
	Options   json.RawMessage `json:"options"`
	Enabled   bool            `json:"enabled"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func scanProvider(row interface{ Scan(...any) error }) (*Provider, error) {
	p := &Provider{}
	var opts []byte
	var created, updated int64
	err := row.Scan(&p.ID, &p.Type, &p.BaseURL, &opts, &p.Enabled, &p.ProxyURL, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Options = opts
	p.CreatedAt, p.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	return p, nil
}

const providerCols = `id,type,base_url,options,enabled,proxy_url,created_at,updated_at`

func (s *Store) GetProvider(ctx context.Context, id string) (*Provider, error) {
	return scanProvider(s.db.QueryRowContext(ctx,
		`SELECT `+providerCols+` FROM providers WHERE id=?`, id))
}

func (s *Store) ListProviders(ctx context.Context) ([]*Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+providerCols+` FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateProvider(ctx context.Context, p *Provider) error {
	if _, err := s.GetProvider(ctx, p.ID); err == nil {
		return ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO providers(`+providerCols+`) VALUES(?,?,?,?,?,?,?,?)`,
		p.ID, p.Type, p.BaseURL, optsOrEmpty(p.Options), p.Enabled, p.ProxyURL, now, now)
	if err != nil {
		return err
	}
	p.CreatedAt, p.UpdatedAt = time.UnixMilli(now), time.UnixMilli(now)
	return nil
}

type ProviderPatch struct {
	Type     *string
	BaseURL  *string
	ProxyURL *string
	Options  *json.RawMessage
	Enabled  *bool
}

func (s *Store) UpdateProvider(ctx context.Context, id string, p ProviderPatch) error {
	sets := []string{"updated_at=?"}
	args := []any{nowMS()}
	if p.Type != nil {
		sets = append(sets, "type=?")
		args = append(args, *p.Type)
	}
	if p.BaseURL != nil {
		sets = append(sets, "base_url=?")
		args = append(args, *p.BaseURL)
	}
	if p.ProxyURL != nil {
		sets = append(sets, "proxy_url=?")
		args = append(args, *p.ProxyURL)
	}
	if p.Options != nil {
		sets = append(sets, "options=?")
		args = append(args, optsOrEmpty(*p.Options))
	}
	if p.Enabled != nil {
		sets = append(sets, "enabled=?")
		args = append(args, *p.Enabled)
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE providers SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return oneRow(res, err)
}

// DeleteProvider refuses (ErrConflict) while accounts or models reference it.
func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	var children int
	err := s.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM accounts WHERE provider_id=?) +
		        (SELECT COUNT(*) FROM models WHERE provider_id=?)`, id, id).Scan(&children)
	if err != nil {
		return err
	}
	if children > 0 {
		return ErrConflict
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id=?`, id)
	return oneRow(res, err)
}

func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
