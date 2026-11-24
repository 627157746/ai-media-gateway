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

func TestModelRename(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	st.CreateProvider(ctx, &Provider{ID: "p1", Type: "t"})
	st.CreateModel(ctx, &Model{ID: "m-old", ProviderID: "p1", Kind: media.KindVideo, Upstream: "u"})
	st.CreateModel(ctx, &Model{ID: "m-taken", ProviderID: "p1", Kind: media.KindImage, Upstream: "u2"})
	st.CreateVideoTask(ctx, &VideoTask{ID: "t1", ModelID: "m-old", ProviderID: "p1", Status: media.TaskInProgress})
	st.CreateVideoTask(ctx, &VideoTask{ID: "t2", ModelID: "other", ProviderID: "p1", Status: media.TaskInProgress})

	newID, takenID, missingSrc := "m-new", "m-taken", "m-x"
	if err := st.UpdateModel(ctx, "m-old", ModelPatch{ID: &newID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetModel(ctx, "m-old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old id still resolves: %v", err)
	}
	m, err := st.GetModel(ctx, "m-new")
	if err != nil || m.Upstream != "u" {
		t.Fatalf("renamed row: %v %+v", err, m)
	}
	tk, _ := st.GetVideoTask(ctx, "t1")
	if tk.ModelID != "m-new" {
		t.Fatalf("task model_id = %q", tk.ModelID)
	}
	tk, _ = st.GetVideoTask(ctx, "t2")
	if tk.ModelID != "other" {
		t.Fatalf("unrelated task rewritten: %q", tk.ModelID)
	}

	// rename onto an existing id conflicts and changes nothing
	if err := st.UpdateModel(ctx, "m-new", ModelPatch{ID: &takenID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if _, err := st.GetModel(ctx, "m-new"); err != nil {
		t.Fatal("conflict clobbered the source row")
	}
	if tk, _ := st.GetVideoTask(ctx, "t1"); tk.ModelID != "m-new" {
		t.Fatalf("conflict rewrote tasks: %q", tk.ModelID)
	}

	// missing old id
	if err := st.UpdateModel(ctx, "ghost", ModelPatch{ID: &missingSrc}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
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
	seal := func(b []byte) ([]byte, error) { return append([]byte("enc:"), b...), nil }
	k, plain, err := st.CreateAPIKey(ctx, "main", seal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "sk-mg-") || len(plain) != len("sk-mg-")+32 {
		t.Fatalf("bad key format %q", plain)
	}
	if !k.Copyable || len(k.KeyEnc) == 0 {
		t.Fatalf("sealed key should be copyable: %+v", k)
	}
	got, err := st.GetAPIKey(ctx, k.ID)
	if err != nil || !got.Copyable || string(got.KeyEnc) != "enc:"+plain {
		t.Fatalf("get: %v %+v", err, got)
	}
	noSeal, _, err := st.CreateAPIKey(ctx, "legacy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if noSeal.Copyable || len(noSeal.KeyEnc) != 0 {
		t.Fatalf("nil seal must not be copyable: %+v", noSeal)
	}
	if got, _ := st.GetAPIKey(ctx, noSeal.ID); got.Copyable {
		t.Fatalf("stored nil-seal key must not be copyable: %+v", got)
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
	st.InsertRequestLog(ctx, &RequestLog{Endpoint: "GET /v1/y", AccountID: "a2", Status: 500, LatencyMs: 7, Error: "e",
		Params: json.RawMessage(`{"prompt":"hi","n":2}`)})
	logs, err := st.ListRequestLogs(ctx, "", time.Time{}, 10)
	if err != nil || len(logs) != 2 {
		t.Fatalf("list: %v %d", err, len(logs))
	}
	// params round-trip: the newest entry carries them, the old one has none
	var p map[string]any
	if err := json.Unmarshal(logs[0].Params, &p); err != nil || p["prompt"] != "hi" || p["n"] != 2.0 {
		t.Fatalf("params: %v %s", err, logs[0].Params)
	}
	if len(logs[1].Params) != 0 {
		t.Fatalf("empty params: %s", logs[1].Params)
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

func TestProxyCRUDAndUsages(t *testing.T) {
	st := open(t)
	ctx := context.Background()

	// create/list/get
	p := &Proxy{Name: "hk", URL: "http://u:p@127.0.0.1:7890"}
	if err := st.CreateProxy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.ID, "px_") {
		t.Fatalf("id: %q", p.ID)
	}
	p2 := &Proxy{Name: "alpha", URL: "socks5://h:1080"}
	st.CreateProxy(ctx, p2)
	list, err := st.ListProxies(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "alpha" {
		t.Fatalf("list: %v %+v", err, list)
	}
	got, err := st.GetProxy(ctx, p.ID)
	if err != nil || got.URL != p.URL {
		t.Fatalf("get: %v %+v", err, got)
	}

	// patch
	nm, u := "hk2", "http://127.0.0.1:8888"
	if err := st.UpdateProxy(ctx, p.ID, &nm, &u); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetProxy(ctx, p.ID)
	if got.Name != "hk2" || got.URL != u {
		t.Fatalf("patched: %+v", got)
	}
	if err := st.UpdateProxy(ctx, "px_ghost", &nm, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("patch missing: %v", err)
	}

	// usages: settings + provider + account
	ref := "proxy:" + p.ID
	st.SetSetting(ctx, "proxy_url", ref)
	st.CreateProvider(ctx, &Provider{ID: "p1", Type: "fal", Enabled: true})
	st.UpdateProvider(ctx, "p1", ProviderPatch{ProxyURL: &ref})
	st.CreateProvider(ctx, &Provider{ID: "p2", Type: "fal", Enabled: true})
	st.CreateAccount(ctx, &Account{ID: "a1", ProviderID: "p1", Name: "main", ProxyURL: ref, Enabled: true})
	st.CreateAccount(ctx, &Account{ID: "a2", ProviderID: "p2", ProxyURL: ref, Enabled: true})
	used, err := st.ProxyUsages(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(used, " | ")
	for _, want := range []string{"全局", "平台 p1", "账号 main（p1）", "账号 a2（p2）"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("usages %v missing %q", used, want)
		}
	}
	used, _ = st.ProxyUsages(ctx, p2.ID)
	if len(used) != 0 {
		t.Fatalf("unused proxy usages: %v", used)
	}

	// delete
	if err := st.DeleteProxy(ctx, p2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetProxy(ctx, p2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
}

// voicesRoundTrip covers the 音色库 CRUD path and the (provider_id,
// upstream_id) uniqueness contract.
func TestVoicesCRUD(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateProvider(ctx, &Provider{ID: "p1", Type: "t", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	v := &Voice{Name: "中文旁白", ProviderID: "p1", UpstreamID: "up-1", Enabled: true}
	if err := st.CreateVoice(ctx, v); err != nil {
		t.Fatal(err)
	}
	if len(v.ID) == 0 || v.ID[:3] != "vc_" {
		t.Fatalf("id: %q", v.ID)
	}

	got, err := st.GetVoice(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "中文旁白" || got.UpstreamID != "up-1" || !got.Enabled {
		t.Fatalf("got: %+v", got)
	}

	// Same provider + same upstream id is a conflict.
	dup := &Voice{Name: "dup", ProviderID: "p1", UpstreamID: "up-1", Enabled: true}
	if err := st.CreateVoice(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup err: %v", err)
	}

	// A different provider may reuse the same upstream id.
	if err := st.CreateProvider(ctx, &Provider{ID: "p2", Type: "t", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	other := &Voice{Name: "other", ProviderID: "p2", UpstreamID: "up-1", Enabled: true}
	if err := st.CreateVoice(ctx, other); err != nil {
		t.Fatalf("cross-provider same upstream should be allowed: %v", err)
	}

	// FindVoice only matches enabled voices of the queried provider.
	if f, err := st.FindVoice(ctx, "p1", v.ID); err != nil || f == nil {
		t.Fatalf("find: %v %v", f, err)
	}
	if f, err := st.FindVoice(ctx, "p2", v.ID); err != nil || f != nil {
		t.Fatalf("find cross-provider should be nil: %v %v", f, err)
	}
	off := false
	if err := st.UpdateVoice(ctx, v.ID, nil, nil, &off); err != nil {
		t.Fatal(err)
	}
	if f, err := st.FindVoice(ctx, "p1", v.ID); err != nil || f != nil {
		t.Fatalf("disabled voice must not resolve: %v %v", f, err)
	}

	if err := st.DeleteVoice(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetVoice(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

// TestDeleteProviderBlocksVoices verifies a provider referenced only by a
// voice is still protected from deletion.
func TestDeleteProviderBlocksVoices(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "vd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateProvider(ctx, &Provider{ID: "p1", Type: "t", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	v := &Voice{Name: "n", ProviderID: "p1", UpstreamID: "u", Enabled: true}
	if err := st.CreateVoice(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProvider(ctx, "p1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if err := st.DeleteVoice(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProvider(ctx, "p1"); err != nil {
		t.Fatalf("after voice delete: %v", err)
	}
}
