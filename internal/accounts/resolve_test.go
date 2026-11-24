package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/store"
)

func setupStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	st.CreateProvider(ctx, &store.Provider{ID: "p1", Type: "t", Enabled: true})
	st.CreateProvider(ctx, &store.Provider{ID: "p2", Type: "t", Enabled: false})
	st.CreateProvider(ctx, &store.Provider{ID: "p3", Type: "t", Options: json.RawMessage(`{"passthrough":false}`), Enabled: true})
	st.CreateModel(ctx, &store.Model{
		ID: "alias", ProviderID: "p1", Kind: media.KindImage, Upstream: "real/model",
		Options: json.RawMessage(`{"accounts":["acc-a"]}`), Enabled: true,
	})
	return st
}

func TestResolveAlias(t *testing.T) {
	st := setupStore(t)
	r, err := Resolve(context.Background(), st, "alias", media.KindImage)
	if err != nil {
		t.Fatal(err)
	}
	if r.Provider.ID != "p1" || r.Model.Upstream != "real/model" || r.Model.Kind != media.KindImage {
		t.Fatalf("%+v", r)
	}
	if len(r.AllowedAccounts) != 1 || r.AllowedAccounts[0] != "acc-a" {
		t.Fatalf("allowlist: %v", r.AllowedAccounts)
	}
}

func TestResolveWrongKind(t *testing.T) {
	st := setupStore(t)
	_, err := Resolve(context.Background(), st, "alias", media.KindVideo)
	if !errors.Is(err, media.ErrWrongKind) {
		t.Fatalf("err: %v", err)
	}
}

func TestResolvePassthrough(t *testing.T) {
	st := setupStore(t)
	r, err := Resolve(context.Background(), st, "p1/bytedance/seedance-2.5", media.KindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model.Upstream != "bytedance/seedance-2.5" || r.Model.Kind != media.KindVideo || r.Model.ID != "p1/bytedance/seedance-2.5" {
		t.Fatalf("%+v", r.Model)
	}
}

func TestResolvePassthroughDisabled(t *testing.T) {
	st := setupStore(t)
	if _, err := Resolve(context.Background(), st, "p3/x", media.KindImage); !errors.Is(err, media.ErrModelNotFound) {
		t.Fatalf("err: %v", err)
	}
	if _, err := Resolve(context.Background(), st, "p2/x", media.KindImage); !errors.Is(err, media.ErrModelNotFound) {
		t.Fatalf("disabled provider: %v", err)
	}
}

func TestResolveNotFound(t *testing.T) {
	st := setupStore(t)
	for _, id := range []string{"nope", "nope/x", "p1/", "/x"} {
		if _, err := Resolve(context.Background(), st, id, media.KindImage); !errors.Is(err, media.ErrModelNotFound) {
			t.Fatalf("%q: %v", id, err)
		}
	}
}
