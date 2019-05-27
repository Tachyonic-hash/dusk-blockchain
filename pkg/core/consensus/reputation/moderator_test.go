package reputation

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/msg"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/crypto"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire/encoding"
)

// This test assures proper functionality of adding strikes to a certain
// committee member, up to the maxStrikes count.
func TestStrikes(t *testing.T) {
	eventBus, removeProvisionerChan := launchModerator()
	// Update round
	consensus.UpdateRound(eventBus, 1)

	// Send enough strikes for one person so we receive something on removeProvisionerChan
	node, _ := crypto.RandEntropy(32)
	for i := uint8(0); i < maxStrikes; i++ {
		publishStrike(1, eventBus, node)
	}

	// We should now receive the public key of the provisioner who has exceeded maxStrikes
	offenderBuf := <-removeProvisionerChan
	assert.Equal(t, offenderBuf.Bytes(), node)
}

// This test assures proper behaviour of the `offenders` map on a round update.
func TestClean(t *testing.T) {
	eventBus, removeProvisionerChan := launchModerator()
	// Update round
	consensus.UpdateRound(eventBus, 1)

	// Add a strike
	node, _ := crypto.RandEntropy(32)
	publishStrike(1, eventBus, node)
	// wait a bit for the referee to strike...
	time.Sleep(time.Millisecond * 100)

	// Update round
	consensus.UpdateRound(eventBus, 2)
	// wait a bit for the referee to update...
	time.Sleep(time.Millisecond * 100)
	// send maxStrikes-1 strikes
	for i := uint8(0); i < maxStrikes-1; i++ {
		publishStrike(2, eventBus, node)
	}

	// check if we get anything on removeProvisionerChan
	timer := time.After(time.Millisecond * 100)
	select {
	case <-removeProvisionerChan:
		assert.Fail(t, "should not have exceeded maxStrikes for the node")
	case <-timer:
		// success
	}
}

func launchModerator() (wire.EventBroker, chan *bytes.Buffer) {
	eventBus := wire.NewEventBus()
	Launch(eventBus)
	removeProvisionerChan := make(chan *bytes.Buffer, 1)
	eventBus.Subscribe(msg.RemoveProvisionerTopic, removeProvisionerChan)
	return eventBus, removeProvisionerChan
}

func publishStrike(round uint64, eb wire.EventBroker, keys ...[]byte) {
	buf := new(bytes.Buffer)
	if err := encoding.WriteUint64(buf, binary.LittleEndian, round); err != nil {
		panic(err)
	}

	if err := encoding.WriteVarInt(buf, uint64(len(keys))); err != nil {
		panic(err)
	}

	for _, key := range keys {
		if err := encoding.WriteVarBytes(buf, key); err != nil {
			panic(err)
		}
	}

	eb.Publish(msg.AbsenteesTopic, buf)
}
