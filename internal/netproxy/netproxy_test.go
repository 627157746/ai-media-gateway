package netproxy

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestEffective(t *testing.T) {
	for _, tc := range []struct{ a, p, g, want string }{
		{"", "", "", ""},
		{"", "", "http://g:1", "http://g:1"},
		{"", "http://p:1", "http://g:1", "http://p:1"},
		{"http://a:1", "http://p:1", "http://g:1", "http://a:1"},
		{"direct", "http://p:1", "http://g:1", "direct"},
	} {
		if got := Effective(tc.a, tc.p, tc.g); got != tc.want {
			t.Fatalf("Effective(%q,%q,%q)=%q want %q", tc.a, tc.p, tc.g, got, tc.want)
		}
	}
}

func TestClientsFor(t *testing.T) {
	c := NewClients(5 * time.Second)

	// direct: no proxy func, api has timeout, stream does not
	api, stream := c.For("direct")
	if api.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("direct should have no proxy func")
	}
	if api.Timeout != 5*time.Second {
		t.Fatalf("api timeout %v", api.Timeout)
	}
	if stream.Timeout != 0 {
		t.Fatalf("stream timeout %v", stream.Timeout)
	}

	// env: ProxyFromEnvironment wired
	envAPI, _ := c.For("")
	req, _ := http.NewRequest("GET", "http://example.com/", nil)
	if _, err := envAPI.Transport.(*http.Transport).Proxy(req); err != nil {
		t.Fatalf("env proxy: %v", err)
	}

	// explicit URL: proxy func returns it
	papi, _ := c.For("socks5h://user:pw@proxy.local:1080")
	pu, err := papi.Transport.(*http.Transport).Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if pu.String() != "socks5h://user:pw@proxy.local:1080" {
		t.Fatalf("proxy url %q", pu)
	}

	// cache: same effective value → same client pointers
	a1, s1 := c.For("http://x:1")
	a2, s2 := c.For("http://x:1")
	if a1 != a2 || s1 != s2 {
		t.Fatal("clients not cached")
	}
}

func TestMask(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"direct", "direct"},
		{"http://proxy:3128", "http://proxy:3128"},
		{"http://user@proxy:3128", "http://user@proxy:3128"},
		{"socks5://user:secret@proxy:1080", "socks5://user:***@proxy:1080"},
	} {
		if got := Mask(tc.in); got != tc.want {
			t.Fatalf("Mask(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
	// url with escaped password still masks
	u, _ := url.Parse("http://u:p%40ss@h:1")
	if got := Mask(u.String()); got == u.String() {
		t.Fatal("escaped password not masked")
	}
}
