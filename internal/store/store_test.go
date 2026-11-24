package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-media-gateway/internal/media"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrationsRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO providers(id,type,created_at,updated_at) VALUES('x','y',1,1)`); err != nil {
		t.Fatalf("providers table missing: %v", err)
	}
	st.db.Exec(`DELETE FROM providers`)
	st.Close()
	// reopening the same DB must be idempotent
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st2.Close()
}

func TestProviderCRUD(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	p := &Provider{ID: "p1", Type: "fal", BaseURL: "https://x", Options: json.RawMessage(`{"a":1}`), Enabled: true}
	if err := st.CreateProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProvider(ctx, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup create: %v", err)
	}
	got, err := st.GetProvider(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "fal" || got.BaseURL != "https://x" || !got.Enabled {
		t.Fatalf("bad provider: %+v", got)
	}
	en := false
	if err := st.UpdateProvider(ctx, "p1", ProviderPatch{Enabled: &en}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetProvider(ctx, "p1")
	if got.Enabled {
		t.Fatal("enabled not patched")
	}
	list, _ := st.ListProviders(ctx)
	if len(list) != 1 {
		t.Fatalf("list = %d", len(list))
	}
	if err := st.DeleteProvider(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetProvider(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	if err := st.DeleteProvider(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestProviderDeleteConflict(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	st.CreateProvider(ctx, &Provider{ID: "p1", Type: "t"})
	st.CreateAccount(ctx, &Account{ID: "a1", ProviderID: "p1", Enabled: true})
	if err := st.DeleteProvider(ctx, "p1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	st.DeleteAccount(ctx, "a1")
	st.CreateModel(ctx, &Model{ID: "m1", ProviderID: "p1", Kind: media.KindImage, Upstream: "u"})
	if err := st.DeleteProvider(ctx, "p1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict (model), got %v", err)
	}
	st.DeleteModel(ctx, "m1")
	if err := st.DeleteProvider(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
}

func TestAccountHealth(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	st.CreateProvider(ctx, &Provider{ID: "p1", Type: "t"})
	a := &Account{ID: "a1", ProviderID: "p1", APIKeyEnc: []byte("enc"), Priority: 2, Weight: 3, Enabled: true}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	until := time.UnixMilli(now.Add(time.Minute).UnixMilli()) // ms-truncated like the DB
	if err := st.RecordFailure(ctx, "a1", "boom", &until, now); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetAccount(ctx, "a1")
	if got.FailCount != 1 || got.FailureCount != 1 || got.CooldownUntil == nil ||
		!got.CooldownUntil.Equal(until) || got.LastError != "boom" {
		t.Fatalf("after failure: %+v", got)
	}
	if err := st.RecordSuccess(ctx, "a1", now); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetAccount(ctx, "a1")
	if got.FailCount != 0 || got.SuccessCount != 1 || got.CooldownUntil != nil || got.LastUsedAt == nil {
		t.Fatalf("after success: %+v", got)
	}
	st.RecordFailure(ctx, "a1", "x", &until, now)
	if err := st.ResetAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetAccount(ctx, "a1")
	if got.FailCount != 0 || got.CooldownUntil != nil || got.LastError != "" {
		t.Fatalf("after reset: %+v", got)
	}
}

func TestAPIKeyAuth(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	k, plain, err := st.CreateAPIKey(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "sk-mg-") || len(plain) != len("sk-mg-")+32 {
		t.Fatalf("bad key format %q", plain)
	}
	auth, err := st.AuthenticateKey(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	if auth.ID != k.ID || auth.LastUsedAt == nil {
		t.Fatalf("auth: %+v", auth)
	}
	if _, err := st.AuthenticateKey(ctx, "sk-mg-wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad key: %v", err)
	}
}

func TestRequestLogs(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	st.InsertRequestLog(ctx, &RequestLog{At: old, Endpoint: "GET /v1/x", AccountID: "a1", Status: 200, LatencyMs: 5})
	st.InsertRequestLog(ctx, &RequestLog{Endpoint: "GET /v1/y", AccountID: "a2", Status: 500, LatencyMs: 7, Error: "e"})
	logs, err := st.ListRequestLogs(ctx, "", time.Time{}, 10)
	if err != nil || len(logs) != 2 {
		t.Fatalf("list: %v %d", err, len(logs))
	}
	logs, _ = st.ListRequestLogs(ctx, "a1", time.Time{}, 10)
	if len(logs) != 1 || logs[0].AccountID != "a1" {
		t.Fatalf("account filter: %+v", logs)
	}
	logs, _ = st.ListRequestLogs(ctx, "", time.Now().Add(-time.Hour), 10)
	if len(logs) != 1 {
		t.Fatalf("since filter: %d", len(logs))
	}
	n, err := st.PruneRequestLogs(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune: %v %d", err, n)
	}
}

// 002_weight_min.sql is a data-fix migration: rows written by older versions
// with weight < 1 are normalized to 1 when the migration applies.
func TestMigrationWeightMin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	mig, err := migrationsFS.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		string(mig),
		`CREATE TABLE IF NOT EXISTS schema_version(version INTEGER NOT NULL)`,
		`INSERT INTO schema_version(version) VALUES(1)`,
		`INSERT INTO providers(id,type,created_at,updated_at) VALUES('p1','t',0,0)`,
		`INSERT INTO accounts(id,provider_id,weight,created_at,updated_at) VALUES('a0','p1',0,0,0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a, err := st.GetAccount(context.Background(), "a0")
	if err != nil {
		t.Fatal(err)
	}
	if a.Weight != 1 {
		t.Fatalf("weight: %d", a.Weight)
	}
}
