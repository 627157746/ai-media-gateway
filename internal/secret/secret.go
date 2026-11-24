package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

var ErrInvalid = errors.New("secret: invalid ciphertext")

// Box encrypts secrets at rest with AES-256-GCM. The key is SHA-256(masterKey).
type Box struct {
	aead cipher.AEAD
}

func New(masterKey string) *Box {
	sum := sha256.Sum256([]byte(masterKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &Box{aead: aead}
}

// Seal returns nonce || ciphertext.
func (b *Box) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

func (b *Box) Open(data []byte) ([]byte, error) {
	if len(data) < b.aead.NonceSize() {
		return nil, ErrInvalid
	}
	nonce, ct := data[:b.aead.NonceSize()], data[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrInvalid
	}
	return plain, nil
}
