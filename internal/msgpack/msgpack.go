// Package msgpack encodes the small subset of MessagePack that Fish Audio's
// zero-shot cloning needs: strings, binary blobs, arrays and maps.
//
// Fish's /v1/tts only accepts references[] (embedded reference audio) as
// application/msgpack — the plain-JSON path has no field for an uploaded
// sample. Rather than pull in another dependency for four types, this mirrors
// the ~60-line encoder the Pi audio plugin uses, so both agree byte for byte.
package msgpack

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Map is an insertion-ordered map: MessagePack maps are ordered on the wire and
// keeping the order makes the encoder deterministic (and test diffs readable).
type Map []Pair

type Pair struct {
	Key string
	Val any
}

// Encode serialises v. Supported types: string, []byte, bool, nil, int,
// float64, []any, and Map.
func Encode(v any) ([]byte, error) {
	var out []byte
	if err := appendValue(&out, v); err != nil {
		return nil, err
	}
	return out, nil
}

func appendValue(dst *[]byte, v any) error {
	switch x := v.(type) {
	case nil:
		*dst = append(*dst, 0xc0)
		return nil
	case bool:
		if x {
			*dst = append(*dst, 0xc3)
		} else {
			*dst = append(*dst, 0xc2)
		}
		return nil
	case string:
		return appendString(dst, x)
	case []byte:
		return appendBinary(dst, x)
	case int:
		return appendInt(dst, int64(x))
	case int64:
		return appendInt(dst, x)
	case float64:
		*dst = append(*dst, 0xcb)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(x))
		*dst = append(*dst, b[:]...)
		return nil
	case []any:
		appendArrayHeader(dst, len(x))
		for _, item := range x {
			if err := appendValue(dst, item); err != nil {
				return err
			}
		}
		return nil
	case Map:
		appendMapHeader(dst, len(x))
		for _, p := range x {
			if err := appendString(dst, p.Key); err != nil {
				return err
			}
			if err := appendValue(dst, p.Val); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("msgpack: unsupported type %T", v)
	}
}

func appendString(dst *[]byte, s string) error {
	n := len(s)
	switch {
	case n <= 31:
		*dst = append(*dst, byte(0xa0|n))
	case n <= 0xff:
		*dst = append(*dst, 0xd9, byte(n))
	case n <= 0xffff:
		*dst = append(*dst, 0xda, byte(n>>8), byte(n))
	default:
		*dst = append(*dst, 0xdb, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	*dst = append(*dst, s...)
	return nil
}

func appendBinary(dst *[]byte, b []byte) error {
	n := len(b)
	switch {
	case n <= 0xff:
		*dst = append(*dst, 0xc4, byte(n))
	case n <= 0xffff:
		*dst = append(*dst, 0xc5, byte(n>>8), byte(n))
	default:
		*dst = append(*dst, 0xc6, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	*dst = append(*dst, b...)
	return nil
}

func appendInt(dst *[]byte, i int64) error {
	switch {
	case i >= 0 && i <= 0x7f:
		*dst = append(*dst, byte(i))
	case i >= -32 && i < 0:
		*dst = append(*dst, byte(0xe0|(i+32)))
	case i >= 0 && i <= 0xff:
		*dst = append(*dst, 0xcc, byte(i))
	case i >= 0 && i <= 0xffff:
		*dst = append(*dst, 0xcd, byte(i>>8), byte(i))
	case i >= 0 && i <= 0xffffffff:
		*dst = append(*dst, 0xce, byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
	default:
		*dst = append(*dst, 0xd3,
			byte(i>>56), byte(i>>48), byte(i>>40), byte(i>>32),
			byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
	}
	return nil
}

func appendArrayHeader(dst *[]byte, n int) {
	switch {
	case n <= 15:
		*dst = append(*dst, byte(0x90|n))
	case n <= 0xffff:
		*dst = append(*dst, 0xdc, byte(n>>8), byte(n))
	default:
		*dst = append(*dst, 0xdd, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
}

func appendMapHeader(dst *[]byte, n int) {
	switch {
	case n <= 15:
		*dst = append(*dst, byte(0x80|n))
	case n <= 0xffff:
		*dst = append(*dst, 0xde, byte(n>>8), byte(n))
	default:
		*dst = append(*dst, 0xdf, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
}
