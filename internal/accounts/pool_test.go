package accounts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-media-gateway/internal/config"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/netproxy"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/secret"
	"ai-media-gateway/internal/store"
)

type fixture struct {
	st  *store.Store
	box *secret.Box
	cur time.Time
	cfg config.Cooldowns
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{
		st:  st,
		box: secret.New("mk"),
		cur: time.UnixMilli(1700000000000),
		cfg: config.Cooldowns{
			RateLimit:   60 * time.Second,
			Upstream:    30 * time.Second,
			UpstreamMax: 10 * time.Minute,
			Quota:       time.Hour,
			Auth:        24 * time.Hour,
		},
	}
	return f
}

func (f *fixture) pool() *Pool {
	cur := &f.cur
	return New(f.st, f.box, f.cfg, netproxy.NewClients(0), func() time.Time { return *cur })
}

func (f *fixture) provider(t *testing.T, id string) {
	t.Helper()
	if err := f.st.CreateProvider(context.Background(), &store.Provider{ID: id, Type: "t"}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) account(t *testing.T, id, provID string, prio, weight int) {
	t.Helper()
	enc, _ := f.box.Seal([]byte("key-" + id))
	a := &store.Account{
		ID: id, ProviderID: provID, APIKeyEnc: enc,
		Priority: prio, Weight: weight, Enabled: true,
	}
	if err := f.st.CreateAccount(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}

func resolved(provID string) Resolved {
	return Resolved{Provider: provider.ProviderConfig{ID: provID}}
}

func ok(calls *[]string) func(context.Context, provider.Call) error {
	return func(_ context.Context, c provider.Call) error {
		*calls = append(*calls, c.Account.ID)
		return nil
	}
}

func TestSWRRWeights(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 2)
	f.account(t, "acc-b", "p1", 0, 1)
	p := f.pool()
	counts := map[string]int{}
	for i := 0; i < 30; i++ {
		var calls []string
		res, err := p.Do(context.Background(), resolved("p1"), ok(&calls))
		if err != nil {
			t.Fatal(err)
		}
		counts[res.AccountID]++
	}
	if counts["acc-a"] != 20 || counts["acc-b"] != 10 {
		t.Fatalf("distribution: %v", counts)
	}
}

func TestPriorityOrdering(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-low", "p1", 0, 10)
	f.account(t, "acc-high", "p1", 5, 1)
	p := f.pool()

	var calls []string
	res, err := p.Do(context.Background(), resolved("p1"), ok(&calls))
	if err != nil || res.AccountID != "acc-high" {
		t.Fatalf("first pick: %v %v", res, err)
	}

	// high fails with failover error → low is tried next in the same Do
	var calls2 []string
	res, err = p.Do(context.Background(), resolved("p1"), func(_ context.Context, c provider.Call) error {
		calls2 = append(calls2, c.Account.ID)
		if c.Account.ID == "acc-high" {
			return &media.UpstreamError{Class: media.ClassUpstream, Status: 500, Message: "boom"}
		}
		return nil
	})
	if err != nil || res.AccountID != "acc-low" || res.Attempts != 2 {
		t.Fatalf("failover: %+v %v calls=%v", res, err, calls2)
	}
	if calls2[0] != "acc-high" || calls2[1] != "acc-low" {
		t.Fatalf("order: %v", calls2)
	}
}

func TestCoolingSkippedAndReturns(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	until := f.cur.Add(time.Minute)
	f.st.RecordFailure(context.Background(), "acc-a", "x", &until, f.cur)
	p := f.pool()

	var calls []string
	if _, err := p.Do(context.Background(), resolved("p1"), ok(&calls)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "acc-b" {
		t.Fatalf("cooling not skipped: %v", calls)
	}

	f.cur = f.cur.Add(2 * time.Minute) // past cooldown
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		res, err := p.Do(context.Background(), resolved("p1"), ok(&calls))
		if err != nil {
			t.Fatal(err)
		}
		seen[res.AccountID] = true
	}
	if !seen["acc-a"] {
		t.Fatal("cooled account did not return")
	}
}

func TestRetryAfterHonored(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	p := f.pool()
	p.Do(context.Background(), resolved("p1"), func(context.Context, provider.Call) error {
		return &media.UpstreamError{Class: media.ClassRateLimit, Status: 429, RetryAfter: 5 * time.Minute}
	})
	got, _ := f.st.GetAccount(context.Background(), "acc-a")
	want := f.cur.Add(5 * time.Minute)
	if got.CooldownUntil == nil || !got.CooldownUntil.Equal(want) {
		t.Fatalf("cooldown=%v want %v", got.CooldownUntil, want)
	}
}

func TestUpstreamBackoffDoublingAndCap(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.cfg.Upstream = 10 * time.Second
	f.cfg.UpstreamMax = 45 * time.Second
	p := f.pool()
	wantDurs := []time.Duration{10, 20, 40, 45, 45}
	for i, want := range wantDurs {
		failAt := f.cur
		_, err := p.Do(context.Background(), resolved("p1"), func(context.Context, provider.Call) error {
			return &media.UpstreamError{Class: media.ClassUpstream, Status: 502}
		})
		var ue *media.UpstreamError
		if !errors.As(err, &ue) {
			t.Fatalf("iter %d: %v", i, err)
		}
		got, _ := f.st.GetAccount(context.Background(), "acc-a")
		exp := failAt.Add(want * time.Second)
		if got.CooldownUntil == nil || !got.CooldownUntil.Equal(exp) {
			t.Fatalf("iter %d: cooldown=%v want %v", i, got.CooldownUntil, exp)
		}
		f.cur = f.cur.Add(want*time.Second + time.Second) // past cooldown
	}
}

func TestAuthCooldown24h(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	p := f.pool()
	p.Do(context.Background(), resolved("p1"), func(context.Context, provider.Call) error {
		return &media.UpstreamError{Class: media.ClassAuth, Status: 401, Message: "bad key"}
	})
	got, _ := f.st.GetAccount(context.Background(), "acc-a")
	if got.CooldownUntil == nil || !got.CooldownUntil.Equal(f.cur.Add(24*time.Hour)) {
		t.Fatalf("cooldown=%v", got.CooldownUntil)
	}
}

func TestSuccessResetsFailCount(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.cfg.Upstream = time.Second
	p := f.pool()
	p.Do(context.Background(), resolved("p1"), func(context.Context, provider.Call) error {
		return &media.UpstreamError{Class: media.ClassUpstream, Status: 500}
	})
	got, _ := f.st.GetAccount(context.Background(), "acc-a")
	if got.FailCount != 1 {
		t.Fatalf("fail_count=%d", got.FailCount)
	}
	f.cur = f.cur.Add(2 * time.Second)
	if _, err := p.Do(context.Background(), resolved("p1"), func(context.Context, provider.Call) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = f.st.GetAccount(context.Background(), "acc-a")
	if got.FailCount != 0 || got.SuccessCount != 1 {
		t.Fatalf("after success: %+v", got)
	}
}

func TestInvalidNoFailoverNoCooldown(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	p := f.pool()
	var calls []string
	_, err := p.Do(context.Background(), resolved("p1"), func(_ context.Context, c provider.Call) error {
		calls = append(calls, c.Account.ID)
		return &media.UpstreamError{Class: media.ClassInvalid, Status: 422, Message: "bad params"}
	})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || ue.Class != media.ClassInvalid {
		t.Fatalf("err: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("failover happened: %v", calls)
	}
	got, _ := f.st.GetAccount(context.Background(), calls[0])
	if got.CooldownUntil != nil || got.FailCount != 0 {
		t.Fatalf("cooled on invalid: %+v", got)
	}
}

func TestFinalNoFailover(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	p := f.pool()
	var calls []string
	final := &media.UpstreamError{Class: media.ClassQuota, Status: 402, Final: true}
	_, err := p.Do(context.Background(), resolved("p1"), func(_ context.Context, c provider.Call) error {
		calls = append(calls, c.Account.ID)
		return final
	})
	if !errors.Is(err, final) || len(calls) != 1 {
		t.Fatalf("err=%v calls=%v", err, calls)
	}
	got, _ := f.st.GetAccount(context.Background(), calls[0])
	if got.CooldownUntil != nil {
		t.Fatal("final error cooled the account")
	}
}

func TestAllCoolingNoAccount(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	soon := f.cur.Add(30 * time.Second)
	later := f.cur.Add(2 * time.Minute)
	f.st.RecordFailure(context.Background(), "acc-a", "x", &later, f.cur)
	f.st.RecordFailure(context.Background(), "acc-b", "x", &soon, f.cur)
	p := f.pool()
	_, err := p.Do(context.Background(), resolved("p1"), func(context.Context, provider.Call) error {
		t.Fatal("should not be called")
		return nil
	})
	var nae *NoAccountError
	if !errors.As(err, &nae) {
		t.Fatalf("err: %v", err)
	}
	if !nae.SoonestRetry.Equal(soon) {
		t.Fatalf("soonest=%v want %v", nae.SoonestRetry, soon)
	}
}

func TestAllDisabledNoAccount(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	en := false
	f.st.UpdateAccount(context.Background(), "acc-a", store.AccountPatch{Enabled: &en})
	p := f.pool()
	_, err := p.Do(context.Background(), resolved("p1"), func(context.Context, provider.Call) error {
		return nil
	})
	var nae *NoAccountError
	if !errors.As(err, &nae) || !nae.SoonestRetry.IsZero() {
		t.Fatalf("err: %v", err)
	}
}

func TestAllowlistRespected(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	p := f.pool()
	m := resolved("p1")
	m.AllowedAccounts = []string{"acc-b"}
	var calls []string
	if _, err := p.Do(context.Background(), m, ok(&calls)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "acc-b" {
		t.Fatalf("calls: %v", calls)
	}
}

func TestSingleAccountSurfacesUpstreamError(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	p := f.pool()
	ue := &media.UpstreamError{Class: media.ClassQuota, Status: 402, Message: "no balance"}
	_, err := p.Do(context.Background(), resolved("p1"), func(context.Context, provider.Call) error {
		return ue
	})
	if !errors.Is(err, ue) {
		t.Fatalf("err: %v", err)
	}
}

func TestCtxCancelStopsWithoutCooldown(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	p := f.pool()
	var calls []string
	_, err := p.Do(context.Background(), resolved("p1"), func(_ context.Context, c provider.Call) error {
		calls = append(calls, c.Account.ID)
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || len(calls) != 1 {
		t.Fatalf("err=%v calls=%v", err, calls)
	}
	got, _ := f.st.GetAccount(context.Background(), calls[0])
	if got.CooldownUntil != nil || got.FailCount != 0 {
		t.Fatalf("cooled on cancel: %+v", got)
	}
}

func TestCallFor(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.provider(t, "p2")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-d", "p1", 0, 1)
	p := f.pool()

	// cooling account still callable
	until := f.cur.Add(time.Hour)
	f.st.RecordFailure(context.Background(), "acc-a", "x", &until, f.cur)
	c, err := p.CallFor(context.Background(), resolved("p1"), "acc-a")
	if err != nil || c.Account.ID != "acc-a" || c.Account.APIKey != "key-acc-a" {
		t.Fatalf("cooling call: %+v %v", c, err)
	}
	// disabled account still callable
	en := false
	f.st.UpdateAccount(context.Background(), "acc-d", store.AccountPatch{Enabled: &en})
	if _, err := p.CallFor(context.Background(), resolved("p1"), "acc-d"); err != nil {
		t.Fatal(err)
	}
	// deleted account errors
	f.st.DeleteAccount(context.Background(), "acc-d")
	if _, err := p.CallFor(context.Background(), resolved("p1"), "acc-d"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	// account of another provider errors
	if _, err := p.CallFor(context.Background(), resolved("p2"), "acc-a"); err == nil {
		t.Fatal("wrong provider accepted")
	}
}

func TestMaxAttemptsCap(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	f.account(t, "acc-c", "p1", 0, 1)
	f.cfg.MaxAttempts = 1
	p := f.pool()
	var calls []string
	_, err := p.Do(context.Background(), resolved("p1"), func(_ context.Context, c provider.Call) error {
		calls = append(calls, c.Account.ID)
		return &media.UpstreamError{Class: media.ClassUpstream, Status: 500}
	})
	if len(calls) != 1 {
		t.Fatalf("cap ignored: %v", calls)
	}
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNonUpstreamErrorStops(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	p := f.pool()
	myErr := errors.New("local bug")
	var calls []string
	_, err := p.Do(context.Background(), resolved("p1"), func(_ context.Context, c provider.Call) error {
		calls = append(calls, c.Account.ID)
		return myErr
	})
	if !errors.Is(err, myErr) || len(calls) != 1 {
		t.Fatalf("err=%v calls=%v", err, calls)
	}
	got, _ := f.st.GetAccount(context.Background(), calls[0])
	if got.CooldownUntil != nil {
		t.Fatal("cooled on non-upstream error")
	}
}

func TestConcurrentDoRaceFree(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	for _, id := range []string{"acc-a", "acc-b", "acc-c"} {
		f.account(t, id, "p1", 0, 1)
	}
	p := f.pool()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 25; j++ {
				var calls []string
				_, _ = p.Do(context.Background(), resolved("p1"), ok(&calls))
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestPrioritySwapResyncsGroups(t *testing.T) {
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 5, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	p := f.pool()
	var calls []string
	res, err := p.Do(context.Background(), resolved("p1"), ok(&calls))
	if err != nil || res.AccountID != "acc-a" {
		t.Fatalf("pre-swap: %+v %v", res, err)
	}
	// swap priorities: same eligible set, different group membership
	five, zero := 0, 5
	f.st.UpdateAccount(context.Background(), "acc-a", store.AccountPatch{Priority: &five})
	f.st.UpdateAccount(context.Background(), "acc-b", store.AccountPatch{Priority: &zero})
	res, err = p.Do(context.Background(), resolved("p1"), ok(&calls))
	if err != nil || res.AccountID != "acc-b" {
		t.Fatalf("post-swap: %+v %v calls=%v", res, err, calls)
	}
}

// Result.AccountID must be set to the last attempted account on every failure
// path so request_logs can record which account was tried.
func TestResultAccountIDOnFailure(t *testing.T) {
	// all attempts fail -> last attempted account
	f := newFixture(t)
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	f.account(t, "acc-b", "p1", 0, 1)
	ue := &media.UpstreamError{Class: media.ClassUpstream, Status: 500, Message: "boom"}
	var calls []string
	res, err := f.pool().Do(context.Background(), resolved("p1"), func(_ context.Context, c provider.Call) error {
		calls = append(calls, c.Account.ID)
		return ue
	})
	if err == nil || len(calls) != 2 || res.AccountID != calls[len(calls)-1] || res.Attempts != 2 {
		t.Fatalf("all-fail: res=%+v calls=%v err=%v", res, calls, err)
	}

	// non-failover error on first attempt -> that account
	f2 := newFixture(t)
	f2.provider(t, "p1")
	f2.account(t, "acc-a", "p1", 0, 1)
	res, err = f2.pool().Do(context.Background(), resolved("p1"), func(_ context.Context, c provider.Call) error {
		return &media.UpstreamError{Class: media.ClassInvalid, Status: 400, Message: "bad"}
	})
	if err == nil || res.Attempts != 1 || res.AccountID != "acc-a" {
		t.Fatalf("invalid-first: res=%+v err=%v", res, err)
	}
}

// A counting httptest server doubles as an HTTP proxy: for a plain-http
// request the transport sends it an absolute-URI GET, which is enough to
// prove which proxy (if any) the call's HTTP client used.
func TestProxyPrecedence(t *testing.T) {
	f := newFixture(t)
	var hitsA, hitsB, hitsG, hitsTarget int32
	mkProxy := func(hits *int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(hits, 1)
			w.WriteHeader(200)
		}))
	}
	proxyA := mkProxy(&hitsA)
	proxyB := mkProxy(&hitsB)
	proxyG := mkProxy(&hitsG)
	defer proxyA.Close()
	defer proxyB.Close()
	defer proxyG.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitsTarget, 1)
		w.WriteHeader(200)
	}))
	defer target.Close()

	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	p := f.pool()
	setAcct := func(v string) {
		if err := f.st.UpdateAccount(context.Background(), "acc-a", store.AccountPatch{ProxyURL: &v}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.SetSetting(context.Background(), "proxy_url", proxyG.URL); err != nil {
		t.Fatal(err)
	}

	get := func(m Resolved) error {
		_, err := p.Do(context.Background(), m, func(ctx context.Context, c provider.Call) error {
			resp, err := c.HTTP.Get(target.URL)
			if err != nil {
				return err
			}
			resp.Body.Close()
			return nil
		})
		return err
	}
	res := Resolved{Provider: provider.ProviderConfig{ID: "p1", ProxyURL: proxyB.URL}}

	// account proxy beats provider+global
	setAcct(proxyA.URL)
	if err := get(res); err != nil || hitsA != 1 || hitsB != 0 || hitsG != 0 {
		t.Fatalf("account level: err=%v A=%d B=%d G=%d", err, hitsA, hitsB, hitsG)
	}
	// account empty → provider
	setAcct("")
	if err := get(res); err != nil || hitsB != 1 || hitsG != 0 {
		t.Fatalf("provider level: err=%v B=%d G=%d", err, hitsB, hitsG)
	}
	// provider empty → global
	if err := get(Resolved{Provider: provider.ProviderConfig{ID: "p1"}}); err != nil || hitsG != 1 {
		t.Fatalf("global level: err=%v G=%d", err, hitsG)
	}
	// "direct" at account level bypasses the provider proxy
	setAcct("direct")
	if err := get(res); err != nil || hitsB != 1 || hitsTarget != 1 {
		t.Fatalf("direct: err=%v B=%d target=%d", err, hitsB, hitsTarget)
	}
}

// proxy:<id> pool references resolve through the store: URL changes take
// effect on the next call, and a deleted entry errors instead of going direct.
func TestProxyPoolReference(t *testing.T) {
	f := newFixture(t)
	var hitsA, hitsB, hitsTarget int32
	mk := func(hits *int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(hits, 1)
			w.WriteHeader(200)
		}))
	}
	proxyA, proxyB := mk(&hitsA), mk(&hitsB)
	defer proxyA.Close()
	defer proxyB.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitsTarget, 1)
	}))
	defer target.Close()

	ctx := context.Background()
	px := &store.Proxy{Name: "p", URL: proxyA.URL}
	if err := f.st.CreateProxy(ctx, px); err != nil {
		t.Fatal(err)
	}
	f.provider(t, "p1")
	f.account(t, "acc-a", "p1", 0, 1)
	ref := "proxy:" + px.ID
	if err := f.st.UpdateAccount(ctx, "acc-a", store.AccountPatch{ProxyURL: &ref}); err != nil {
		t.Fatal(err)
	}
	p := f.pool()
	get := func() error {
		_, err := p.Do(ctx, resolved("p1"), func(ctx context.Context, c provider.Call) error {
			resp, err := c.HTTP.Get(target.URL)
			if err != nil {
				return err
			}
			resp.Body.Close()
			return nil
		})
		return err
	}

	// reference routes through the pool URL
	if err := get(); err != nil || hitsA != 1 {
		t.Fatalf("ref: err=%v hitsA=%d", err, hitsA)
	}
	// updating the pool URL takes effect next call
	if err := f.st.UpdateProxy(ctx, px.ID, nil, &proxyB.URL); err != nil {
		t.Fatal(err)
	}
	if err := get(); err != nil || hitsB != 1 || hitsA != 1 {
		t.Fatalf("re-pointed: err=%v A=%d B=%d", err, hitsA, hitsB)
	}
	// deleted entry → error, no silent direct
	if err := f.st.DeleteProxy(ctx, px.ID); err != nil {
		t.Fatal(err)
	}
	err := get()
	if err == nil || !strings.Contains(err.Error(), "已被删除") || hitsTarget != 0 {
		t.Fatalf("dangling ref: err=%v target=%d", err, hitsTarget)
	}
}
