package selection

import (
	"time"

	"github.com/dusk-network/dusk-blockchain/pkg/config"

	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/header"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/key"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/user"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/block"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/transactions"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/message"
	crypto "github.com/dusk-network/dusk-crypto/hash"
)

// ProvisionerNr is the default amount of Provisioners utilized in the
// selection tests. This nr is just used to create the RoundUpdate and bares no
// importance in the selection step
var ProvisionerNr = 10

// Helper for reducing selection test boilerplate
type Helper struct {
	*consensus.Emitter
	Round        uint64
	Step         uint8
	scoreToSpawn int
	P            *user.Provisioners
}

// NewHelper creates a Helper
func NewHelper(scoreToSpawn int) *Helper {
	p, provisionersKeys := consensus.MockProvisioners(ProvisionerNr)
	mockProxy := transactions.MockProxy{
		P: transactions.PermissiveProvisioner{},
	}
	emitter := consensus.MockEmitter(time.Second, mockProxy)
	emitter.Keys = provisionersKeys[0]

	hlp := &Helper{
		Emitter:      emitter,
		Round:        uint64(1),
		Step:         uint8(1),
		scoreToSpawn: scoreToSpawn,
		P:            p,
	}
	return hlp
}

// RoundUpdate mocks a round update with the Round and Step embedded in the
// Helper
func (h *Helper) RoundUpdate() consensus.RoundUpdate {
	hash, _ := crypto.RandEntropy(32)
	seed, _ := crypto.RandEntropy(32)

	return consensus.RoundUpdate{
		Round: h.Round,
		Hash:  hash,
		Seed:  seed,
		P:     *h.P,
	}
}

// Spawn a number of score events.
func (h *Helper) Spawn() []message.Score {
	evs := make([]message.Score, 0, h.scoreToSpawn)
	for i := 0; i < h.scoreToSpawn; i++ {
		hash, _ := crypto.RandEntropy(32)
		keys, _ := key.NewRandKeys()
		hdr := header.Header{
			Round:     h.Round,
			Step:      h.Step,
			PubKeyBLS: keys.BLSPubKeyBytes,
			BlockHash: hash,
		}
		genesis := config.DecodeGenesis()
		cert := block.EmptyCertificate()
		candidate := message.MakeCandidate(genesis, cert)
		evs = append(evs, message.MockScore(hdr, candidate))
	}
	return evs
}
