package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Proxy is a named, reusable proxy URL from the 代理池.
type Proxy struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const proxyCols = `id,name,url,created_at,updated_at`

func scanProxy(row interface{ Scan(...any) error }) (*Proxy, error) {
	p := &Proxy{}
	var created, updated int64
	err := row.Scan(&p.ID, &p.Name, &p.URL, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt, p.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	return p, nil
}

func (s *Store) GetProxy(ctx context.Context, id string) (*Proxy, error) {
	return scanProxy(s.db.QueryRowContext(ctx,
		`SELECT `+proxyCols+` FROM proxies WHERE id=?`, id))
}

// ListProxies returns all pool proxies ordered by name.
func (s *Store) ListProxies(ctx context.Context) ([]*Proxy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+proxyCols+` FROM proxies ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Proxy{}
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateProxy(ctx context.Context, p *Proxy) error {
	if p.ID == "" {
		p.ID = "px_" + RandHex(6)
	}
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO proxies(`+proxyCols+`) VALUES(?,?,?,?,?)`,
		p.ID, p.Name, p.URL, now, now)
	if err != nil {
		return err
	}
	p.CreatedAt, p.UpdatedAt = time.UnixMilli(now), time.UnixMilli(now)
	return nil
}

// UpdateProxy patches name/url; nil = keep.
func (s *Store) UpdateProxy(ctx context.Context, id string, name, url *string) error {
	sets := []string{"updated_at=?"}
	args := []any{nowMS()}
	if name != nil {
		sets = append(sets, "name=?")
		args = append(args, *name)
	}
	if url != nil {
		sets = append(sets, "url=?")
		args = append(args, *url)
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE proxies SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return oneRow(res, err)
}

func (s *Store) DeleteProxy(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id=?`, id)
	return oneRow(res, err)
}

// ProxyUsages returns human labels for everything referencing "proxy:<id>":
// 全局 (settings), 平台 <id> (providers), 账号 <name||id>（<provider>） (accounts).
func (s *Store) ProxyUsages(ctx context.Context, id string) ([]string, error) {
	ref := "proxy:" + id
	var out []string

	if v, err := s.GetSetting(ctx, "proxy_url"); err != nil {
		return nil, err
	} else if v == ref {
		out = append(out, "全局")
	}

	provs, err := s.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range provs {
		if p.ProxyURL == ref {
			out = append(out, fmt.Sprintf("平台 %s", p.ID))
		}
	}

	accs, err := s.ListAccounts(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, a := range accs {
		if a.ProxyURL == ref {
			name := a.Name
			if name == "" {
				name = a.ID
			}
			out = append(out, fmt.Sprintf("账号 %s（%s）", name, a.ProviderID))
		}
	}
	return out, nil
}
