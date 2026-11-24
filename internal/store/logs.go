package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type RequestLog struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	Endpoint   string          `json:"endpoint"`
	ModelID    string          `json:"model_id,omitempty"`
	ProviderID string          `json:"provider_id,omitempty"`
	AccountID  string          `json:"account_id,omitempty"`
	Attempts   int             `json:"attempts"`
	Status     int             `json:"status"`
	LatencyMs  int64           `json:"latency_ms"`
	Error      string          `json:"error,omitempty"`
	Params     json.RawMessage `json:"params,omitempty"`
}

func (s *Store) InsertRequestLog(ctx context.Context, l *RequestLog) error {
	if l.At.IsZero() {
		l.At = time.Now()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO request_logs(at,endpoint,model_id,provider_id,account_id,attempts,status,latency_ms,error,params)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		l.At.UnixMilli(), l.Endpoint, strOrNil(l.ModelID), strOrNil(l.ProviderID),
		strOrNil(l.AccountID), l.Attempts, l.Status, l.LatencyMs, strOrNil(l.Error),
		strOrNil(string(l.Params)))
	if err != nil {
		return err
	}
	l.ID, _ = res.LastInsertId()
	return nil
}

// ListRequestLogs lists newest first; account "" = all, since zero = all.
func (s *Store) ListRequestLogs(ctx context.Context, account string, since time.Time, limit int) ([]*RequestLog, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id,at,endpoint,model_id,provider_id,account_id,attempts,status,latency_ms,error,params
	      FROM request_logs WHERE 1=1`
	var args []any
	if account != "" {
		q += ` AND account_id=?`
		args = append(args, account)
	}
	if !since.IsZero() {
		q += ` AND at>=?`
		args = append(args, since.UnixMilli())
	}
	q += ` ORDER BY at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RequestLog
	for rows.Next() {
		l := &RequestLog{}
		var at int64
		var modelID, provID, accID, errStr, params sql.NullString
		if err := rows.Scan(&l.ID, &at, &l.Endpoint, &modelID, &provID, &accID,
			&l.Attempts, &l.Status, &l.LatencyMs, &errStr, &params); err != nil {
			return nil, err
		}
		l.At = time.UnixMilli(at)
		l.ModelID, l.ProviderID, l.AccountID, l.Error = modelID.String, provID.String, accID.String, errStr.String
		l.Params = json.RawMessage(params.String)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) PruneRequestLogs(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM request_logs WHERE at<?`, olderThan.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
