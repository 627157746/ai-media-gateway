package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"ai-media-gateway/internal/media"
)

// ReadErrorBody reads up to 64KB of an error response body.
func ReadErrorBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return b
}

// ParseRetryAfter reads a Retry-After header (seconds or HTTP date).
func ParseRetryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// isDialError reports whether nothing reached the vendor (DNS/dial/connect).
func isDialError(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// TransportError wraps a client-side (network/timeout) failure.
// submit=true marks it Final unless it was a pure dial error: after bytes may
// have reached the vendor, retrying could create a duplicate paid job.
func TransportError(err error, submit bool) *media.UpstreamError {
	return &media.UpstreamError{
		Class:   media.ClassUpstream,
		Status:  0,
		Message: err.Error(),
		Final:   submit && !isDialError(err),
	}
}

// FetchURL performs an unauthenticated GET of a result URL, forwarding an
// optional Range header. Caller closes the body.
func FetchURL(ctx context.Context, client *http.Client, url, rangeHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	return client.Do(req)
}
