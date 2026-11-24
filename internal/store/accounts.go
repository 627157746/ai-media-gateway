package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Account carries the encrypted API key (api_key_enc); callers decrypt it.
type Account struct {
	ID            string
	ProviderID    string
	Name          string
	APIKeyEnc     []byte
	BaseURL       string
	Options       json.RawMessage
	Priority      int
	Weight        int
	Enabled       bool
	CooldownUntil *time.Time
	FailCount     int64
	SuccessCount  int64
	FailureCount  int64
	LastError     string
	LastErrorAt   *time.Time
	LastUsedAt    *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const accountCols = `id,provider_id,name,api_key_enc,base_url,options,priority,weight,enabled,
	cooldown_until,fail_count,success_count,failure_count,last_error,last_error_at,last_used_at,created_at,updated_at`

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	a := &Account{}
	var base sql.NullString
	var opts []byte
	var cool, errAt, usedAt sql.NullInt64
	var lastErr sql.NullString
	var created, updated int64
	err := row.Scan(&a.ID, &a.ProviderID, &a.Name, &a.APIKeyEnc, &base, &opts,
		&a.Priority, &a.Weight, &a.Enabled, &cool, &a.FailCount, &a.SuccessCount,
		&a.FailureCount, &lastErr, &errAt, &usedAt, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.BaseURL = base.String
	a.Options = opts
	a.CooldownUntil = timeFromMS(cool)
	a.LastErrorAt = timeFromMS(errAt)
	a.LastUsedAt = timeFromMS(usedAt)
	a.LastError = lastErr.String
	a.CreatedAt, a.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	return a, nil
}

func (s *Store) GetAccount(ctx context.Context, id string) (*Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx,
		`SELECT `+accountCols+` FROM accounts WHERE id=?`, id))
}

// ListAccounts returns accounts for providerID, or all when providerID is "".
func (s *Store) ListAccounts(ctx context.Context, providerID string) ([]*Account, error) {
	q := `SELECT ` + accountCols + ` FROM accounts`
	var rows *sql.Rows
	var err error
	if providerID == "" {
		rows, err = s.db.QueryContext(ctx, q+` ORDER BY id`)
	} else {
		rows, err = s.db.QueryContext(ctx, q+` WHERE provider_id=? ORDER BY priority DESC, id`, providerID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CreateAccount(ctx context.Context, a *Account) error {
	if _, err := s.GetAccount(ctx, a.ID); err == nil {
		return ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO accounts(`+accountCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ProviderID, a.Name, a.APIKeyEnc, strOrNil(a.BaseURL), optsOrEmpty(a.Options),
		a.Priority, a.Weight, a.Enabled, msOrNil(a.CooldownUntil), 0, 0, 0,
		nil, nil, nil, now, now)
	if err != nil {
		return err
	}
	a.CreatedAt, a.UpdatedAt = time.UnixMilli(now), time.UnixMilli(now)
	return nil
}

type AccountPatch struct {
	Name      *string
	APIKeyEnc *[]byte // re-sealed by caller
	BaseURL   *string
	Options   *json.RawMessage
	Priority  *int
	Weight    *int
	Enabled   *bool
}

func (s *Store) UpdateAccount(ctx context.Context, id string, p AccountPatch) error {
	sets := []string{"updated_at=?"}
	args := []any{nowMS()}
	add := func(col string, v any) {
		sets = append(sets, col+"=?")
		args = append(args, v)
	}
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.APIKeyEnc != nil {
		add("api_key_enc", *p.APIKeyEnc)
	}
	if p.BaseURL != nil {
		add("base_url", strOrNil(*p.BaseURL))
	}
	if p.Options != nil {
		add("options", optsOrEmpty(*p.Options))
	}
	if p.Priority != nil {
		add("priority", *p.Priority)
	}
	if p.Weight != nil {
		add("weight", *p.Weight)
	}
	if p.Enabled != nil {
		add("enabled", *p.Enabled)
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return oneRow(res, err)
}

func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, id)
	return oneRow(res, err)
}

// RecordSuccess clears consecutive-failure state on a successful call.
func (s *Store) RecordSuccess(ctx context.Context, id string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET fail_count=0, success_count=success_count+1,
		 last_used_at=?, cooldown_until=NULL, updated_at=? WHERE id=?`,
		now.UnixMilli(), now.UnixMilli(), id)
	return oneRow(res, err)
}

// RecordFailure bumps counters and applies the computed cooldown.
func (s *Store) RecordFailure(ctx context.Context, id string, errMsg string, cooldownUntil *time.Time, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET failure_count=failure_count+1, fail_count=fail_count+1,
		 last_error=?, last_error_at=?, cooldown_until=?, last_used_at=?, updated_at=? WHERE id=?`,
		errMsg, now.UnixMilli(), msOrNil(cooldownUntil), now.UnixMilli(), now.UnixMilli(), id)
	return oneRow(res, err)
}

// ResetAccount clears cooldown and error state (admin action).
func (s *Store) ResetAccount(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET cooldown_until=NULL, fail_count=0, last_error=NULL,
		 last_error_at=NULL, updated_at=? WHERE id=?`, nowMS(), id)
	return oneRow(res, err)
}
