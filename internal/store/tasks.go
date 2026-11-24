package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-media-gateway/internal/media"
)

type VideoTask struct {
	ID           string           `json:"id"`
	ModelID      string           `json:"model_id"`
	ProviderID   string           `json:"provider_id"`
	AccountID    string           `json:"account_id"`
	Handle       media.TaskHandle `json:"upstream_handle"`
	Status       media.TaskStatus `json:"status"`
	Progress     int              `json:"progress"`
	ResultURL    string           `json:"result_url,omitempty"`
	Error        string           `json:"error,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	CompletedAt  *time.Time       `json:"completed_at"`
	LastPolledAt *time.Time       `json:"last_polled_at"`
}

const taskCols = `id,model_id,provider_id,account_id,upstream_handle,status,progress,
	result_url,error,created_at,updated_at,completed_at,last_polled_at`

func scanTask(row interface{ Scan(...any) error }) (*VideoTask, error) {
	t := &VideoTask{}
	var status string
	var handle []byte
	var url, errStr sql.NullString
	var created, updated int64
	var completed, polled sql.NullInt64
	err := row.Scan(&t.ID, &t.ModelID, &t.ProviderID, &t.AccountID, &handle,
		&status, &t.Progress, &url, &errStr, &created, &updated, &completed, &polled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Status = media.TaskStatus(status)
	t.Handle = handle
	t.ResultURL = url.String
	t.Error = errStr.String
	t.CreatedAt, t.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	t.CompletedAt = timeFromMS(completed)
	t.LastPolledAt = timeFromMS(polled)
	return t, nil
}

func (s *Store) CreateVideoTask(ctx context.Context, t *VideoTask) error {
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO video_tasks(`+taskCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.ModelID, t.ProviderID, t.AccountID, string(t.Handle), string(t.Status),
		t.Progress, strOrNil(t.ResultURL), strOrNil(t.Error), now, now, nil, nil)
	if err != nil {
		return err
	}
	t.CreatedAt, t.UpdatedAt = time.UnixMilli(now), time.UnixMilli(now)
	return nil
}

func (s *Store) GetVideoTask(ctx context.Context, id string) (*VideoTask, error) {
	return scanTask(s.db.QueryRowContext(ctx,
		`SELECT `+taskCols+` FROM video_tasks WHERE id=?`, id))
}

// UpdateVideoTask writes the mutable fields of an existing task.
func (s *Store) UpdateVideoTask(ctx context.Context, t *VideoTask) error {
	t.UpdatedAt = time.UnixMilli(nowMS())
	res, err := s.db.ExecContext(ctx,
		`UPDATE video_tasks SET status=?, progress=?, result_url=?, error=?,
		 updated_at=?, completed_at=?, last_polled_at=? WHERE id=?`,
		string(t.Status), t.Progress, strOrNil(t.ResultURL), strOrNil(t.Error),
		t.UpdatedAt.UnixMilli(), msOrNil(t.CompletedAt), msOrNil(t.LastPolledAt), t.ID)
	return oneRow(res, err)
}

// ListVideoTasks lists newest first; status "" = all.
func (s *Store) ListVideoTasks(ctx context.Context, status string, limit int) ([]*VideoTask, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + taskCols + ` FROM video_tasks`
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.QueryContext(ctx, q+` ORDER BY created_at DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, q+` WHERE status=? ORDER BY created_at DESC LIMIT ?`, status, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*VideoTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
