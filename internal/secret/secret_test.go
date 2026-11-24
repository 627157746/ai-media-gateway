package secret

import (
	"bytes"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	b := New("master-key")
	plain := []byte("sk-vendor-abc123")
	enc, err := b.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(enc, plain) {
		t.Fatal("ciphertext equals plaintext")
	}
	dec, err := b.Open(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, plain) {
		t.Fatalf("got %q want %q", dec, plain)
	}
	// non-deterministic nonce
	enc2, _ := b.Seal(plain)
	if bytes.Equal(enc, enc2) {
		t.Fatal("same plaintext produced same ciphertext")
	}
}

func TestTamper(t *testing.T) {
	b := New("master-key")
	enc, _ := b.Seal([]byte("secret"))
	enc[len(enc)-1] ^= 0xff
	if _, err := b.Open(enc); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered: got %v", err)
	}
	if _, err := b.Open([]byte("short")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short: got %v", err)
	}
	if _, err := New("other-key").Open(enc); !errors.Is(err, ErrInvalid) {
		// note: enc is tampered; also wrong key — either way must fail
		t.Fatalf("wrong key: got %v", err)
	}
}
