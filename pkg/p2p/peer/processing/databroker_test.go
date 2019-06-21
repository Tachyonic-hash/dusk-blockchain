package processing_test

import (
	"bytes"
	"testing"

	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/block"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/peer/peermsg"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/peer/processing"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire/topics"
)

// Test the behaviour of the data broker
func TestSendData(t *testing.T) {
	fn := mockConfig(t)
	defer fn()

	// Set up db
	// TODO: use a mock for this instead
	drvr, db := setupDatabase()
	defer drvr.Close()
	defer db.Close()

	// Generate 5 blocks and store them in the db, and save the hashes for later checking.
	hashes, blocks := generateBlocks(t, 5, db)
	if err := storeBlocks(db, blocks); err != nil {
		t.Fatal(err)
	}

	responseChan := make(chan *bytes.Buffer, 100)
	dataBroker := processing.NewDataBroker(db, responseChan)

	// Make a GetData and give it to the dataBroker
	msg := createGetDataBuffer(hashes...)
	if err := dataBroker.SendItems(msg); err != nil {
		t.Fatal(err)
	}

	// We should receive 5 new blocks from the peer
	var recvBlocks []*block.Block
	for i := 0; i < 5; i++ {
		buf := <-responseChan

		// Check for correctness of topic
		topic := extractTopic(buf)
		if topic != topics.Block {
			t.Fatalf("unexpected topic %s, expected Block", topic)
		}

		// Decode block
		blk := block.NewBlock()
		if err := blk.Decode(buf); err != nil {
			t.Fatal(err)
		}

		recvBlocks = append(recvBlocks, blk)
	}

	// Check that block hashes match up with those we generated
	for i, blk := range recvBlocks {
		if !bytes.Equal(hashes[i], blk.Header.Hash) {
			t.Fatal("received block has mismatched hash")
		}
	}
}

// TODO: probably specify somewhere a choice between block and tx type
func createGetDataBuffer(hashes ...[]byte) *bytes.Buffer {
	inv := &peermsg.Inv{}
	for _, hash := range hashes {
		inv.AddItem(peermsg.InvTypeBlock, hash)
	}

	buf := new(bytes.Buffer)
	if err := inv.Encode(buf); err != nil {
		panic(err)
	}

	return buf
}
