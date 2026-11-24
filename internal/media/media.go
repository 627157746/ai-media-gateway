package media

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

type Kind string

const (
	KindImage  Kind = "image"
	KindVideo  Kind = "video"
	KindSpeech Kind = "speech"
)

func (k Kind) Valid() bool {
	return k == KindImage || k == KindVideo || k == KindSpeech
}

// Input is one uploaded media blob. When URL is set it is passed through
// untouched (the gateway never fetches it — no SSRF).
type Input struct {
	Name string
	MIME string
	Data []byte
	URL  string
}

func (in Input) DataURL() string {
	if in.URL != "" {
		return in.URL
	}
	mime := in.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(in.Data)
}

func ParseDataURL(s string) (Input, error) {
	if !strings.HasPrefix(s, "data:") {
		return Input{}, fmt.Errorf("%w: not a data URL", ErrBadRequest)
	}
	rest := s[len("data:"):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return Input{}, fmt.Errorf("%w: malformed data URL", ErrBadRequest)
	}
	meta, payload := rest[:comma], rest[comma+1:]
	parts := strings.Split(meta, ";")
	mime := parts[0]
	var raw []byte
	var err error
	b64 := false
	for _, p := range parts[1:] {
		if p == "base64" {
			b64 = true
		}
	}
	if b64 {
		raw, err = base64.StdEncoding.DecodeString(payload)
	} else {
		var un string
		un, err = url.PathUnescape(payload)
		raw = []byte(un)
	}
	if err != nil {
		return Input{}, fmt.Errorf("%w: malformed data URL payload", ErrBadRequest)
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	return Input{MIME: mime, Data: raw}, nil
}

type ImageRequest struct {
	Prompt         string
	N              int
	Size           string
	Quality        string
	Background     string
	Resolution     string
	ResponseFormat string
	Edit           bool
	Images         []Input
	Mask           *Input
	Extra          map[string]any
}

type Asset struct {
	URL  string
	B64  string
	MIME string
}

type ImageResult struct {
	Images []Asset
}

type VideoRequest struct {
	Prompt        string
	Mode          string
	Seconds       string
	Size          string
	Resolution    string
	Ratio         string
	GenerateAudio *bool
	Watermark     *bool
	FirstFrame    *Input
	LastFrame     *Input
	References    []Input
	Videos        []Input
	Audios        []Input
	Extra         map[string]any
}

// TaskHandle is opaque vendor state for a submitted video task.
type TaskHandle json.RawMessage

type TaskStatus string

const (
	TaskQueued     TaskStatus = "queued"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
	TaskFailed     TaskStatus = "failed"
	TaskCancelled  TaskStatus = "cancelled"
)

type TaskState struct {
	Status   TaskStatus
	Progress int
	URL      string
	Error    string
}

type SpeechRequest struct {
	Input        string
	Voice        string
	Format       string
	Instructions string
	Speed        float64
	Extra        map[string]any
}

type Audio struct {
	Body        io.ReadCloser
	ContentType string
}

type ErrorClass int

const (
	ClassAuth ErrorClass = iota
	ClassQuota
	ClassRateLimit
	ClassUpstream
	ClassInvalid
	ClassContent
)

func (c ErrorClass) String() string {
	switch c {
	case ClassAuth:
		return "auth"
	case ClassQuota:
		return "quota"
	case ClassRateLimit:
		return "rate_limit"
	case ClassUpstream:
		return "upstream"
	case ClassInvalid:
		return "invalid"
	case ClassContent:
		return "content"
	}
	return "unknown"
}

// Failover reports whether another account is worth trying for this class.
func (c ErrorClass) Failover() bool {
	switch c {
	case ClassAuth, ClassQuota, ClassRateLimit, ClassUpstream:
		return true
	}
	return false
}

type UpstreamError struct {
	Class      ErrorClass
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
	// Final marks errors that must never fail over even if Class would.
	Final bool
}

func (e *UpstreamError) Error() string {
	msg := fmt.Sprintf("%s: %s", e.Class, e.Message)
	if e.Status > 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if e.Code != "" {
		msg += fmt.Sprintf(" [%s]", e.Code)
	}
	return msg
}

var (
	ErrBadRequest    = errors.New("bad request")
	ErrModelNotFound = errors.New("model not found")
	ErrWrongKind     = errors.New("model kind does not match endpoint")
)
