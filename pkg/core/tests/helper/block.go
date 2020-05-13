package helper

import (
	"testing"
	"time"

	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/key"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/block"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/transactions"
	"github.com/dusk-network/dusk-crypto/bls"
	"github.com/stretchr/testify/assert"
)

// RandomBlock returns a random block for testing.
// For `height` see also helper.RandomHeader
// For txBatchCount see also helper.RandomSliceOfTxs
func RandomBlock(height uint64, txBatchCount uint16) *block.Block {
	b := &block.Block{
		Header: RandomHeader(height),
		Txs:    transactions.RandContractCalls(int(txBatchCount), 0, true),
	}
	hash, err := b.CalculateHash()
	if err != nil {
		panic(err)
	}
	b.Header.Hash = hash
	root, xerr := b.CalculateRoot()
	if xerr != nil {
		panic(xerr)
	}
	b.Header.TxRoot = root
	return b
}

// TwoLinkedBlocks returns two blocks that are linked via their headers
func TwoLinkedBlocks(t *testing.T) (*block.Block, *block.Block) {
	blk0 := &block.Block{
		Header: RandomHeader(200),
		Txs:    transactions.RandContractCalls(19, 0, true),
	}
	hash, err := blk0.CalculateHash()
	assert.Nil(t, err)
	blk0.Header.Hash = hash

	blk1 := &block.Block{
		Header: RandomHeader(200),
		Txs:    transactions.RandContractCalls(19, 0, true),
	}

	blk1.Header.PrevBlockHash = blk0.Header.Hash
	blk1.Header.Height = blk0.Header.Height + 1
	blk1.Header.Timestamp = blk0.Header.Timestamp + 100
	root, err := blk1.CalculateRoot()
	assert.Nil(t, err)
	blk1.Header.TxRoot = root
	hash, err = blk1.CalculateHash()
	assert.Nil(t, err)
	blk1.Header.Hash = hash

	return blk0, blk1
}

// RandomCertificate returns a random block certificate for testing
func RandomCertificate() *block.Certificate {
	return block.EmptyCertificate()
}

// RandomHeader returns a random header for testing. `height` randomness is up
// to the caller. A global atomic counter per pkg can handle it
func RandomHeader(height uint64) *block.Header {

	h := &block.Header{
		Version:   0,
		Height:    height,
		Timestamp: time.Now().Unix(),

		PrevBlockHash: transactions.Rand32Bytes(),
		Seed:          RandomBLSSignature(),
		TxRoot:        transactions.Rand32Bytes(),

		Certificate: RandomCertificate(),
	}

	return h
}

// GenesisMock should mock a valid genesis block. For now, it just creates a
// random block, but this should change to a more sophisticated Genesis
// FIXME: 417 - create a believable Genesis block
func GenesisMock(t *testing.T, txNr uint16) *block.Block {
	return RandomBlock(0, txNr)
}

// RandomBLSSignature returns a valid BLS Signature of a bogus message
func RandomBLSSignature() []byte {
	msg := "this is a test"
	keys, _ := key.NewRandKeys()
	sig, err := bls.Sign(keys.BLSSecretKey, keys.BLSPubKey, []byte(msg))
	if err != nil {
		panic(err)
	}
	return sig.Compress()
}
