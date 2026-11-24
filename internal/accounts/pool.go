package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-media-gateway/internal/config"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/secret"
	"ai-media-gateway/internal/store"
)

// Resolved is a model routed to a provider, with an optional account allowlist.
type Resolved struct {
	Provider        provider.ProviderConfig
	Model           provider.ModelConfig
	AllowedAccounts []string
}

type Result struct {
	AccountID string
	Attempts  int
}

// NoAccountError means no account is eligible right now.
// SoonestRetry is the earliest enabled account's cooldown_until (zero if none enabled).
type NoAccountError struct {
	ProviderID   string
	SoonestRetry time.Time
}

func (e *NoAccountError) Error() string {
	if e.SoonestRetry.IsZero() {
		return fmt.Sprintf("no enabled account available for provider %q", e.ProviderID)
	}
	return fmt.Sprintf("all accounts for provider %q are cooling down; retry at %s",
		e.ProviderID, e.SoonestRetry.UTC().Format(time.RFC3339))
}

// Pool selects accounts, runs attempts with failover, and tracks health.
type Pool struct {
	st     *store.Store
	box    *secret.Box
	cfg    config.Cooldowns
	client *http.Client
	stream *http.Client
	now    func() time.Time

	mu sync.Mutex
	// SWRR state per eligible-set key ("provider|id1,id2,..."), rebuilt on change.
	rr map[string]*rrState
}

func New(st *store.Store, box *secret.Box, cfg config.Cooldowns, client *http.Client, now func() time.Time) *Pool {
	if now == nil {
		now = time.Now
	}
	return &Pool{st: st, box: box, cfg: cfg, client: client, stream: &http.Client{}, now: now, rr: map[string]*rrState{}}
}

// Do runs fn against eligible accounts until success or a non-failover error.
func (p *Pool) Do(ctx context.Context, m Resolved, fn func(ctx context.Context, c provider.Call) error) (Result, error) {
	accs, err := p.st.ListAccounts(ctx, m.Provider.ID)
	if err != nil {
		return Result{}, err
	}
	eligible := p.eligible(accs, m, p.now())
	if len(eligible) == 0 {
		return Result{}, &NoAccountError{ProviderID: m.Provider.ID, SoonestRetry: soonestRetry(accs, p.now())}
	}
	attempts := p.attemptOrder(m.Provider.ID, eligible)
	if p.cfg.MaxAttempts > 0 && len(attempts) > p.cfg.MaxAttempts {
		attempts = attempts[:p.cfg.MaxAttempts]
	}

	var lastErr error
	var lastUE *media.UpstreamError
	var lastID string // last attempted account, for request_logs
	for i, a := range attempts {
		if err := ctx.Err(); err != nil {
			return Result{AccountID: lastID, Attempts: i}, err
		}
		call, err := p.callFor(a, m)
		if err != nil {
			return Result{AccountID: a.ID, Attempts: i}, err
		}
		lastID = a.ID
		err = fn(ctx, call)
		if err == nil {
			_ = p.st.RecordSuccess(context.WithoutCancel(ctx), a.ID, p.now())
			return Result{AccountID: a.ID, Attempts: i + 1}, nil
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{AccountID: lastID, Attempts: i + 1}, err
		}
		var ue *media.UpstreamError
		if !errors.As(err, &ue) || !ue.Class.Failover() || ue.Final {
			return Result{AccountID: lastID, Attempts: i + 1}, err
		}
		lastUE = ue
		p.cool(ctx, a, ue)
		lastErr = err
	}
	// All attempts failed: surface the last vendor error so single-account
	// providers return the real upstream failure.
	if lastUE != nil {
		return Result{AccountID: lastID, Attempts: len(attempts)}, lastUE
	}
	return Result{AccountID: lastID, Attempts: len(attempts)}, lastErr
}

// CallFor builds a Call for a specific account (task affinity): the account is
// used even when disabled or cooling; a deleted account is an error.
func (p *Pool) CallFor(ctx context.Context, m Resolved, accountID string) (provider.Call, error) {
	a, err := p.st.GetAccount(ctx, accountID)
	if err != nil {
		return provider.Call{}, fmt.Errorf("account %s: %w", accountID, err)
	}
	if a.ProviderID != m.Provider.ID {
		return provider.Call{}, fmt.Errorf("account %s does not belong to provider %s", accountID, m.Provider.ID)
	}
	return p.callFor(a, m)
}

func (p *Pool) callFor(a *store.Account, m Resolved) (provider.Call, error) {
	call := provider.Call{
		Provider: m.Provider,
		Model:    m.Model,
		HTTP:     p.client,
		Stream:   p.stream,
		Timeout:  p.cfg.ImageTimeout,
		Account: provider.AccountConfig{
			ID:      a.ID,
			Name:    a.Name,
			BaseURL: a.BaseURL,
			Options: a.Options,
		},
	}
	if len(a.APIKeyEnc) > 0 {
		key, err := p.box.Open(a.APIKeyEnc)
		if err != nil {
			return provider.Call{}, fmt.Errorf("decrypt key of account %s: %w", a.ID, err)
		}
		call.Account.APIKey = string(key)
	}
	return call, nil
}

func (p *Pool) eligible(accs []*store.Account, m Resolved, now time.Time) []*store.Account {
	var allowed map[string]bool
	if len(m.AllowedAccounts) > 0 {
		allowed = map[string]bool{}
		for _, id := range m.AllowedAccounts {
			allowed[id] = true
		}
	}
	var out []*store.Account
	for _, a := range accs {
		if !a.Enabled {
			continue
		}
		if a.CooldownUntil != nil && a.CooldownUntil.After(now) {
			continue
		}
		if allowed != nil && !allowed[a.ID] {
			continue
		}
		out = append(out, a)
	}
	return out
}

func soonestRetry(accs []*store.Account, now time.Time) time.Time {
	var soonest time.Time
	for _, a := range accs {
		if !a.Enabled || a.CooldownUntil == nil || !a.CooldownUntil.After(now) {
			continue
		}
		if soonest.IsZero() || a.CooldownUntil.Before(soonest) {
			soonest = *a.CooldownUntil
		}
	}
	return soonest
}

// cool applies the class-specific cooldown and records the failure.
func (p *Pool) cool(ctx context.Context, a *store.Account, ue *media.UpstreamError) {
	now := p.now()
	var until *time.Time
	var d time.Duration
	switch ue.Class {
	case media.ClassRateLimit:
		d = p.cfg.RateLimit
		if ue.RetryAfter > 0 {
			d = ue.RetryAfter
		}
	case media.ClassUpstream:
		// base * 2^(fail_count-1) capped at max; fail_count here is the
		// consecutive count after this failure = a.FailCount+1.
		shift := a.FailCount
		if shift > 62 {
			shift = 62
		}
		d = p.cfg.Upstream * (1 << shift)
		if d <= 0 || d > p.cfg.UpstreamMax {
			d = p.cfg.UpstreamMax
		}
	case media.ClassQuota:
		d = p.cfg.Quota
	case media.ClassAuth:
		d = p.cfg.Auth
	}
	if d > 0 {
		t := now.Add(d)
		until = &t
	}
	_ = p.st.RecordFailure(context.WithoutCancel(ctx), a.ID, ue.Error(), until, now)
}

// ---- smooth weighted round-robin (nginx style) ----

type rrNode struct {
	weight int
	cur    int
}

type rrGroup struct {
	// nodes keyed by account id; order for deterministic iteration.
	order []string
	nodes map[string]*rrNode
}

type rrState struct {
	groups map[int]*rrGroup // by priority
}

const maxRRStates = 256

// attemptOrder returns eligible accounts ordered for attempts: for each
// priority group (desc), the SWRR pick first then the rest by current weight.
func (p *Pool) attemptOrder(providerID string, eligible []*store.Account) []*store.Account {
	ids := make([]string, len(eligible))
	byID := make(map[string]*store.Account, len(eligible))
	for i, a := range eligible {
		ids[i] = a.ID
		byID[a.ID] = a
	}
	sort.Strings(ids)
	key := providerID + "|" + strings.Join(ids, ",")

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.rr) > maxRRStates {
		p.rr = map[string]*rrState{}
	}
	state := p.rr[key]
	if state == nil {
		state = &rrState{groups: map[int]*rrGroup{}}
		p.rr[key] = state
	}

	// group eligible accounts by priority (desc)
	var prios []int
	groups := map[int][]string{}
	for _, id := range ids {
		prio := byID[id].Priority
		if _, ok := groups[prio]; !ok {
			prios = append(prios, prio)
		}
		groups[prio] = append(groups[prio], id)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(prios)))

	var out []*store.Account
	for _, prio := range prios {
		g := state.groups[prio]
		if g == nil {
			g = &rrGroup{nodes: map[string]*rrNode{}}
			state.groups[prio] = g
		}
		// resync the group when membership differs (members are sorted ids)
		members := groups[prio]
		same := len(g.order) == len(members)
		if same {
			for i, id := range members {
				if g.order[i] != id {
					same = false
					break
				}
			}
		}
		if !same {
			g.nodes = map[string]*rrNode{}
			g.order = append(g.order[:0], members...)
		}
		for _, id := range members {
			w := byID[id].Weight
			if w <= 0 {
				w = 1
			}
			n := g.nodes[id]
			if n == nil {
				n = &rrNode{}
				g.nodes[id] = n
			}
			n.weight = w
		}
		pick, rest := g.pick()
		out = append(out, byID[pick])
		for _, id := range rest {
			out = append(out, byID[id])
		}
	}
	return out
}

// pick advances the group and returns the chosen id plus the remaining ids in
// descending current-weight order.
func (g *rrGroup) pick() (string, []string) {
	total := 0
	for _, n := range g.nodes {
		n.cur += n.weight
		total += n.weight
	}
	best := ""
	for _, id := range g.order {
		if best == "" || g.nodes[id].cur > g.nodes[best].cur {
			best = id
		}
	}
	g.nodes[best].cur -= total
	rest := make([]string, 0, len(g.order)-1)
	for _, id := range g.order {
		if id != best {
			rest = append(rest, id)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		return g.nodes[rest[i]].cur > g.nodes[rest[j]].cur
	})
	return best, rest
}
