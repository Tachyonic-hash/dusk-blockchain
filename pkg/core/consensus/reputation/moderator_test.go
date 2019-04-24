package reputation

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/msg"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/crypto"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire"
)

// This test assures proper functionality of adding strikes to a certain
// committee member, up to the maxStrikes count.
func TestStrikes(t *testing.T) {
	eventBus, removeProvisionerChan := launchModerator()

	// Send enough strikes for one person so we receive something on removeProvisionerChan
	node, _ := crypto.RandEntropy(32)
	for i := uint8(0); i < maxStrikes; i++ {
		eventBus.Publish(msg.AbsenteesTopic, bytes.NewBuffer(node))
	}

	// We should now receive the public key of the provisioner who has exceeded maxStrikes
	offenderBuf := <-removeProvisionerChan
	assert.Equal(t, offenderBuf.Bytes(), node)
}

// This test assures proper behaviour of the `offenders` map on a round update.
func TestClean(t *testing.T) {
	eventBus, removeProvisionerChan := launchModerator()

	// Add a strike
	node, _ := crypto.RandEntropy(32)
	eventBus.Publish(msg.AbsenteesTopic, bytes.NewBuffer(node))
	// wait a bit for the referee to strike...
	time.Sleep(time.Millisecond * 100)

	// Update round
	consensus.UpdateRound(eventBus, 2)
	// wait a bit for the referee to update...
	time.Sleep(time.Millisecond * 100)
	// send maxStrikes-1 strikes
	for i := uint8(0); i < maxStrikes-1; i++ {
		eventBus.Publish(msg.AbsenteesTopic, bytes.NewBuffer(node))
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
	LaunchReputationComponent(eventBus)
	removeProvisionerChan := make(chan *bytes.Buffer, 1)
	eventBus.Subscribe(msg.RemoveProvisionerTopic, removeProvisionerChan)
	return eventBus, removeProvisionerChan
}