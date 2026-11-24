// Package netproxy resolves the 3-level proxy settings (account > provider >
// global) into cached http.Client pairs.
//
// A proxy value is one of:
//   - ""        inherit / fall through to the next level (and ultimately to
//     the HTTPS_PROXY-style environment variables)
//   - "direct"  no proxy, overriding the level above
//   - URL       http, https, socks5 or socks5h proxy URL
package netproxy

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Effective returns the first non-empty of account, provider, global.
func Effective(account, provider, global string) string {
	if account != "" {
		return account
	}
	if provider != "" {
		return provider
	}
	return global
}

// Clients caches one api+stream client pair per effective proxy value so
// transports (and connection pools) are shared between requests.
type Clients struct {
	timeout time.Duration
	mu      sync.Mutex
	m       map[string][2]*http.Client
}

// NewClients builds the cache; api clients get timeout, stream clients none.
func NewClients(timeout time.Duration) *Clients {
	return &Clients{timeout: timeout, m: map[string][2]*http.Client{}}
}

// For returns the (api, stream) clients for an effective proxy value.
func (c *Clients) For(effective string) (api, stream *http.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pair, ok := c.m[effective]; ok {
		return pair[0], pair[1]
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	switch effective {
	case "":
		tr.Proxy = http.ProxyFromEnvironment
	case "direct":
		tr.Proxy = nil
	default:
		if u, err := url.Parse(effective); err == nil {
			tr.Proxy = http.ProxyURL(u)
		} else {
			tr.Proxy = http.ProxyFromEnvironment
		}
	}
	pair := [2]*http.Client{
		{Transport: tr, Timeout: c.timeout},
		{Transport: tr},
	}
	c.m[effective] = pair
	return pair[0], pair[1]
}

// Mask returns the proxy URL with its userinfo password replaced by ***,
// for display. "" and "direct" pass through unchanged.
func Mask(u string) string {
	if u == "" || u == "direct" {
		return u
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.User == nil {
		return u
	}
	if _, has := parsed.User.Password(); has {
		parsed.User = url.UserPassword(parsed.User.Username(), "***")
	}
	// URL.String() escapes * to %2A in the password; undo it for display.
	return strings.ReplaceAll(parsed.String(), "%2A%2A%2A", "***")
}
