package wallet

import (
	"bytes"
	"errors"
	"math/big"

	"github.com/decred/base58"
	"golang.org/x/crypto/ed25519"
)

var (
	// PubKeyPrefix is the prefix needed to produce an Adress
	// that starts with DUSKpub
	// This will usually be followed by a number of 1s
	// which is the padding
	PubKeyPrefix = big.NewInt((int64(0x265CC5580E64)))
)

// PubKey is a wrapper around the ed25519 public key
type PubKey struct {
	ed25519.PublicKey
}

// Verify method wraps around the native ed25519 function
func (p *PubKey) Verify(message, sig []byte) bool {
	return ed25519.Verify(p.PublicKey, message, sig)
}

// Address returns the Base58 encoding of a public key
// Format will start with DUSK
func (p *PubKey) PublicAddress() (string, error) {
	if len(p.PublicKey) != 32 {
		return "", errors.New("Pubkey length does not equal 32")
	}
	return KeyToAddress(PubKeyPrefix, p.PublicKey, 2)
}

func KeyToAddress(prefix *big.Int, pub []byte, padding int) (string, error) {
	buf := new(bytes.Buffer)

	buf.Write(prefix.Bytes())
	pad := make([]byte, padding)
	buf.Write(pad)
	buf.Write(pub)

	checksum, err := Checksum(pub)
	if err != nil {
		return "", errors.New("Could not calculate the checksum")
	}
	buf.Write(checksum)

	WIF := base58.Encode(buf.Bytes())
	return WIF, nil
}