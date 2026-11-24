package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"-"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

const keyCols = `id,name,key_hash,enabled,created_at,last_used_at`

func keyHash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// RandHex returns n random bytes as hex.
func RandHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// CreateAPIKey returns the row and the plaintext key 鈥?shown exactly once.
func (s *Store) CreateAPIKey(ctx context.Context, name string) (*APIKey, string, error) {
	plaintext := "sk-mg-" + RandHex(16)
	k := &APIKey{
		ID:        "key_" + RandHex(6),
		Name:      name,
		KeyHash:   keyHash(plaintext),
		Enabled:   true,
		CreatedAt: time.UnixMilli(nowMS()),
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys(`+keyCols+`) VALUES(?,?,?,?,?,?)`,
		k.ID, k.Name, k.KeyHash, k.Enabled, k.CreatedAt.UnixMilli(), nil)
	if err != nil {
		return nil, "", err
	}
	return k, plaintext, nil
}

func scanKey(row interface{ Scan(...any) error }) (*APIKey, error) {
	k := &APIKey{}
	var usedAt sql.NullInt64
	var created int64
	err := row.Scan(&k.ID, &k.Name, &k.KeyHash, &k.Enabled, &created, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	k.CreatedAt = time.UnixMilli(created)
	k.LastUsedAt = timeFromMS(usedAt)
	return k, nil
}

// AuthenticateKey validates a plaintext gateway key and stamps last_used_at.
func (s *Store) AuthenticateKey(ctx context.Context, plaintext string) (*APIKey, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx,
		`SELECT `+keyCols+` FROM api_keys WHERE key_hash=? AND enabled=1`, keyHash(plaintext)))
	if err != nil {
		return nil, err
	}
	now := time.UnixMilli(nowMS())
	_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=?`, now.UnixMilli(), k.ID)
	k.LastUsedAt = &now
	return k, nil
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyCols+` FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id=?`, id)
	return oneRow(res, err)
}
