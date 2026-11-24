package msgpack

import (
	"bytes"
	"testing"
)

// TestEncodeFishReferencePayload pins the exact bytes of the payload Fish
// Audio receives for zero-shot cloning. The vendor decodes this, so a silent
// encoding change would break synthesis without any Go test noticing — hence
// hand-written expectations instead of a round-trip check.
func TestEncodeFishReferencePayload(t *testing.T) {
	payload := Map{
		{"text", "hi"},
		{"references", []any{
			Map{
				{"audio", []byte{0x01, 0x02, 0x03}},
				{"text", ""},
			},
		}},
		{"format", "mp3"},
	}
	got, err := Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x83,                     // fixmap, 3 entries
		0xa4, 't', 'e', 'x', 't', // "text"
		0xa2, 'h', 'i', // "hi"
		0xaa, 'r', 'e', 'f', 'e', 'r', 'e', 'n', 'c', 'e', 's', // "references"
		0x91,                          // fixarray, 1 element
		0x82,                          // fixmap, 2 entries
		0xa5, 'a', 'u', 'd', 'i', 'o', // "audio"
		0xc4, 0x03, 0x01, 0x02, 0x03, // bin8 + 3 bytes
		0xa4, 't', 'e', 'x', 't', // "text"
		0xa0,                               // fixstr, length 0
		0xa6, 'f', 'o', 'r', 'm', 'a', 't', // "format"
		0xa3, 'm', 'p', '3', // "mp3"
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload mismatch\n got %x\nwant %x", got, want)
	}
}

func TestEncodeScalars(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []byte
	}{
		{"nil", nil, []byte{0xc0}},
		{"true", true, []byte{0xc3}},
		{"false", false, []byte{0xc2}},
		{"small int", 5, []byte{0x05}},
		{"neg int", -1, []byte{0xff}},
		{"uint8", 200, []byte{0xcc, 0xc8}},
		{"short str", "abc", []byte{0xa3, 'a', 'b', 'c'}},
		{"empty str", "", []byte{0xa0}},
		{"bin8", []byte{0xde, 0xad}, []byte{0xc4, 0x02, 0xde, 0xad}},
	}
	for _, c := range cases {
		got, err := Encode(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !bytes.Equal(got, c.want) {
			t.Fatalf("%s: got %x want %x", c.name, got, c.want)
		}
	}
}

// TestEncodeLargeBinary covers the bin16 branch a real 100 KB voice sample hits.
func TestEncodeLargeBinary(t *testing.T) {
	data := make([]byte, 300)
	got, err := Encode(data)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 0xc5 || got[1] != 0x01 || got[2] != 0x2c {
		t.Fatalf("bin16 header: %x", got[:3])
	}
	if len(got) != 3+300 {
		t.Fatalf("len %d", len(got))
	}
}

func TestEncodeUnsupported(t *testing.T) {
	if _, err := Encode(struct{}{}); err == nil {
		t.Fatal("expected error for unsupported type")
	}
}
