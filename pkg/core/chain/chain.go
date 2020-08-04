package chain

import (
	"bytes"
	"context"
	"errors"
	"time"

	"encoding/binary"
	"sync"

	"github.com/dusk-network/dusk-blockchain/pkg/config"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/block"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/transactions"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/peer/peermsg"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/peer/processing/chainsync"
	"github.com/dusk-network/dusk-blockchain/pkg/util/nativeutils/eventbus"
	"github.com/dusk-network/dusk-blockchain/pkg/util/nativeutils/rpcbus"
	"github.com/dusk-network/dusk-protobuf/autogen/go/node"
	logger "github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/user"

	//"github.com/dusk-network/dusk-blockchain/pkg/core/data/transactions"
	"github.com/dusk-network/dusk-blockchain/pkg/core/verifiers"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/encoding"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/message"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/topics"
)

var log = logger.WithFields(logger.Fields{"process": "chain"})

// Verifier performs checks on the blockchain and potentially new incoming block
type Verifier interface {
	// PerformSanityCheck on first N blocks and M last blocks
	PerformSanityCheck(startAt uint64, firstBlocksAmount uint64, lastBlockAmount uint64) error
	// SanityCheckBlock will verify whether a block is valid according to the rules of the consensus
	SanityCheckBlock(prevBlock block.Block, blk block.Block) error
}

// Loader is an interface which abstracts away the storage used by the Chain to
// store the blockchain
type Loader interface {
	// LoadTip of the chain
	LoadTip() (*block.Block, error)
	// Clear removes everything from the DB
	Clear() error
	// Close the Loader and finalizes any pending connection
	Close(driver string) error
	// Height returns the current height as stored in the loader
	Height() (uint64, error)
	// BlockAt returns the block at a given height
	BlockAt(uint64) (block.Block, error)
	// Append a block on the storage
	Append(*block.Block) error
}

// Chain represents the nodes blockchain
// This struct will be aware of the current state of the node.
type Chain struct {
	eventBus *eventbus.EventBus
	rpcBus   *rpcbus.RPCBus
	p        *user.Provisioners
	counter  *chainsync.Counter

	// loader abstracts away the persistence aspect of Block operations
	loader Loader

	// verifier performs verifications on the block
	verifier Verifier

	prevBlock block.Block
	// protect prevBlock with mutex as it's touched out of the main chain loop
	// by SubscribeCallback.
	// TODO: Consider if mutex can be removed
	mu sync.RWMutex

	// Intermediate block, decided on by consensus.
	// Used to verify a candidate against the correct previous block,
	// and to be accepted once a certificate is decided on.
	intermediateBlock *block.Block

	// Most recent certificate generated by the Agreement component.
	// Held on the Chain, to be requested by the block generator,
	// for including it with the candidate message.
	lastCertificate *block.Certificate

	// BLS keys of the most recent committee, responsible for finalizing the intermediate block.
	lastCommittee [][]byte

	// The highest block we've seen from the network. This is updated
	// by the synchronizer, and used to calculate our synchronization
	// progress.
	highestSeen uint64

	// collector channels
	certificateChan <-chan certMsg
	highestSeenChan <-chan uint64

	// rusk client
	executor transactions.Executor

	// rpcbus channels
	getLastBlockChan         <-chan rpcbus.Request
	verifyCandidateBlockChan <-chan rpcbus.Request
	getLastCertificateChan   <-chan rpcbus.Request
	getRoundResultsChan      <-chan rpcbus.Request
	getLastCommitteeChan     <-chan rpcbus.Request

	ctx context.Context
}

// New returns a new chain object. It accepts the EventBus (for messages coming
// from (remote) consensus components, the RPCBus for dispatching synchronous
// data related to Certificates, Blocks, Rounds and progress. It also accepts a
// counter to manage the synchronization process and the hash of the genesis
// block
// TODO: the counter should be encapsulated in a specific component for
// synchronization
func New(ctx context.Context, eventBus *eventbus.EventBus, rpcBus *rpcbus.RPCBus, counter *chainsync.Counter, loader Loader, verifier Verifier, srv *grpc.Server, executor transactions.Executor) (*Chain, error) {
	// set up collectors
	certificateChan := initCertificateCollector(eventBus)
	highestSeenChan := initHighestSeenCollector(eventBus)

	// set up rpcbus channels
	getLastBlockChan := make(chan rpcbus.Request, 1)
	verifyCandidateBlockChan := make(chan rpcbus.Request, 1)
	getLastCertificateChan := make(chan rpcbus.Request, 1)
	getRoundResultsChan := make(chan rpcbus.Request, 1)
	getLastCommitteeChan := make(chan rpcbus.Request, 1)
	if err := rpcBus.Register(topics.GetLastBlock, getLastBlockChan); err != nil {
		return nil, err
	}
	if err := rpcBus.Register(topics.VerifyCandidateBlock, verifyCandidateBlockChan); err != nil {
		return nil, err
	}
	if err := rpcBus.Register(topics.GetLastCertificate, getLastCertificateChan); err != nil {
		return nil, err
	}
	if err := rpcBus.Register(topics.GetRoundResults, getRoundResultsChan); err != nil {
		return nil, err
	}
	if err := rpcBus.Register(topics.GetLastCommittee, getLastCommitteeChan); err != nil {
		return nil, err
	}

	chain := &Chain{
		eventBus:                 eventBus,
		rpcBus:                   rpcBus,
		p:                        user.NewProvisioners(),
		counter:                  counter,
		certificateChan:          certificateChan,
		highestSeenChan:          highestSeenChan,
		getLastBlockChan:         getLastBlockChan,
		verifyCandidateBlockChan: verifyCandidateBlockChan,
		getLastCertificateChan:   getLastCertificateChan,
		getRoundResultsChan:      getRoundResultsChan,
		getLastCommitteeChan:     getLastCommitteeChan,
		lastCommittee:            make([][]byte, 0),
		loader:                   loader,
		verifier:                 verifier,
		executor:                 executor,
		ctx:                      ctx,
	}

	prevBlock, err := loader.LoadTip()
	if err != nil {
		return nil, err
	}

	// If the `prevBlock` is genesis, we add an empty intermediate block.
	if prevBlock.Header.Height == 0 {
		// TODO: maybe it would be better to have a consensus-compatible
		// intermediate block and certificate.
		chain.lastCertificate = block.EmptyCertificate()
		blk, err := mockFirstIntermediateBlock(prevBlock.Header)
		if err != nil {
			return nil, err
		}

		chain.intermediateBlock = blk

		// If we're running the test harness, we should also populate some consensus values
		if config.Get().Genesis.Legacy {
			if err := setupBidValues(); err != nil {
				return nil, err
			}

			if err := reconstructCommittee(chain.p, prevBlock); err != nil {
				return nil, err
			}
		}
	}
	chain.prevBlock = *prevBlock

	if srv != nil {
		node.RegisterChainServer(srv, chain)
	}

	// Hook the chain up to the required topics
	cbListener := eventbus.NewSafeCallbackListener(chain.onAcceptBlock)
	eventBus.Subscribe(topics.Block, cbListener)
	initListener := eventbus.NewSafeCallbackListener(chain.onInitialization)
	eventBus.Subscribe(topics.Initialization, initListener)
	return chain, nil
}

// Listen to the collectors
func (c *Chain) Listen() {
	for {
		select {
		case certificateMsg := <-c.certificateChan:
			c.handleCertificateMessage(certificateMsg)
		case height := <-c.highestSeenChan:
			c.highestSeen = height
		case r := <-c.getLastBlockChan:
			c.provideLastBlock(r)
		case r := <-c.verifyCandidateBlockChan:
			c.processCandidateVerificationRequest(r)
		case r := <-c.getLastCertificateChan:
			c.provideLastCertificate(r)
		case r := <-c.getRoundResultsChan:
			c.provideRoundResults(r)
		case r := <-c.getLastCommitteeChan:
			c.provideLastCommittee(r)
		case <-c.ctx.Done():
			// TODO: dispose the Chain
		}
	}
}

func (c *Chain) onAcceptBlock(m message.Message) error {
	// Ignore blocks from peers if we are only one behind - we are most
	// likely just about to finalize consensus.
	// TODO: we should probably just accept it if consensus was not
	// started yet

	// Accept the block
	blk := m.Payload().(block.Block)

	field := logger.Fields{"process": "onAcceptBlock", "height": blk.Header.Height}
	lg := log.WithFields(field)

	if !c.counter.IsSyncing() {
		lg.Error("could not accept block since we are syncing")
		return nil
	}

	// If we are more than one block behind, stop the consensus
	lg.Debug("topics.StopConsensus")
	c.eventBus.Publish(topics.StopConsensus, message.New(topics.StopConsensus, nil))

	// This will decrement the sync counter
	// TODO: a new context should be created with timeout, cancellation, etc
	// instead of reusing the Chain global one
	if err := c.AcceptBlock(c.ctx, blk); err != nil {
		lg.WithError(err).Debug("could not AcceptBlock")
		return err
	}

	// If we are no longer syncing after accepting this block,
	// request a certificate and intermediate block for the
	// second to last round.
	if !c.counter.IsSyncing() {
		blk, cert, err := c.requestRoundResults(blk.Header.Height + 1)
		if err != nil {
			lg.WithError(err).Debug("could not requestRoundResults")
			return err
		}

		c.intermediateBlock = blk
		c.lastCertificate = cert

		// Once received, we can re-start consensus.
		// This sets off a chain of processing which goes from sending the
		// round update, to re-instantiating the consensus, to setting off
		// the first consensus loop. So, we do this in a goroutine to
		// avoid blocking other requests to the chain.
		go func() {
			err = c.sendRoundUpdate()
			if err != nil {
				lg.WithError(err).Debug("could not sendRoundUpdate")
			}
		}()
	}

	return nil
}

// AcceptBlock will accept a block if
// 1. We have not seen it before
// 2. All stateless and stateful checks are true
// Returns nil, if checks passed and block was successfully saved
func (c *Chain) AcceptBlock(ctx context.Context, blk block.Block) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	field := logger.Fields{"process": "accept block", "height": blk.Header.Height}
	l := log.WithFields(field)

	l.Trace("verifying block")

	// 1. Check that stateless and stateful checks pass
	if err := c.verifier.SanityCheckBlock(c.prevBlock, blk); err != nil {
		l.WithError(err).Error("block verification failed")
		return err
	}

	// 2. Check the certificate
	// This check should avoid a possible race condition between accepting two blocks
	// at the same height, as the probability of the committee creating two valid certificates
	// for the same round is negligible.
	l.Trace("verifying block certificate")
	if err := verifiers.CheckBlockCertificate(*c.p, blk); err != nil {
		l.WithError(err).Error("certificate verification failed")
		return err
	}

	// 3. Call ExecuteStateTransitionFunction
	l.Debug("calling ExecuteStateTransitionFunction")
	provisioners, err := c.executor.ExecuteStateTransition(ctx, blk.Txs, blk.Header.Height)
	if err != nil {
		l.WithError(err).Error("Error in executing the state transition")
		return err
	}

	// Caching the provisioners and bidList
	c.p = &provisioners

	// 4. Store the approved block
	l.Trace("storing block in db")
	if err := c.loader.Append(&blk); err != nil {
		l.WithError(err).Error("block storing failed")
		return err
	}

	// 5. Gossip advertise block Hash
	l.Trace("gossiping block")
	if err := c.advertiseBlock(blk); err != nil {
		l.WithError(err).Error("block advertising failed")
		return err
	}

	c.prevBlock = blk

	// 6. Notify other subsystems for the accepted block
	// Subsystems listening for this topic:
	// mempool.Mempool
	// consensus.generation.broker
	l.Trace("notifying internally")

	msg := message.New(topics.AcceptedBlock, blk)
	c.eventBus.Publish(topics.AcceptedBlock, msg)

	l.Trace("procedure ended")
	return nil
}

func (c *Chain) onInitialization(message.Message) error {
	return c.sendRoundUpdate()
}

func (c *Chain) sendRoundUpdate() error {
	hdr := c.intermediateBlock.Header

	ru := consensus.RoundUpdate{
		Round: hdr.Height + 1,
		P:     *c.p,
		Seed:  hdr.Seed,
		Hash:  hdr.Hash,
	}
	log.
		WithField("round", ru.Round).
		Debug("sendRoundUpdate, topics.RoundUpdate")
	msg := message.New(topics.RoundUpdate, ru)
	c.eventBus.Publish(topics.RoundUpdate, msg)
	return nil
}

func (c *Chain) processCandidateVerificationRequest(r rpcbus.Request) {
	var res rpcbus.Response
	// We need to verify the candidate block against the newest
	// intermediate block. The intermediate block would be the most
	// recent block before the candidate.
	if c.intermediateBlock == nil {
		res.Err = errors.New("no intermediate block hash known")
		r.RespChan <- res
		return
	}
	cm := r.Params.(message.Candidate)

	// We first perform a quick check on the Block Header and
	if err := c.verifier.SanityCheckBlock(*c.intermediateBlock, *cm.Block); err != nil {
		res.Err = err
		r.RespChan <- res
		return
	}

	calls, err := c.executor.ValidateStateTransition(c.ctx, c.intermediateBlock.Txs, c.intermediateBlock.Header.Height)
	if err != nil {
		res.Err = err
		r.RespChan <- res
		return
	}

	if len(calls) != len(c.intermediateBlock.Txs) {
		res.Err = errors.New("block contains invalid transactions")
	}

	r.RespChan <- res
}

// Send Inventory message to all peers
func (c *Chain) advertiseBlock(b block.Block) error {
	msg := &peermsg.Inv{}
	msg.AddItem(peermsg.InvTypeBlock, b.Header.Hash)

	buf := new(bytes.Buffer)
	if err := msg.Encode(buf); err != nil {
		//TODO: shall this really panic ?
		log.Panic(err)
	}

	if err := topics.Prepend(buf, topics.Inv); err != nil {
		//TODO: shall this really panic ?
		log.Panic(err)
	}

	m := message.New(topics.Inv, *buf)
	c.eventBus.Publish(topics.Gossip, m)
	return nil
}

func (c *Chain) handleCertificateMessage(cMsg certMsg) {

	// Set latest certificate and committee
	c.lastCertificate = cMsg.cert
	c.lastCommittee = cMsg.committee

	// Fetch new intermediate block and corresponding certificate
	resp, err := c.rpcBus.Call(topics.GetCandidate, rpcbus.NewRequest(*bytes.NewBuffer(cMsg.hash)), 5*time.Second)
	if err != nil {
		// If the we can't get the block, we will fall
		// back and catch up later.
		log.
			WithError(err).
			WithField("height", c.highestSeen).
			Error("could not find winning candidate block")
		return
	}
	cm := resp.(message.Candidate)

	if c.intermediateBlock == nil {
		// If we're missing the intermediate block, we will also fall
		// back and catch up later.
		log.Warnln("intermediate block is missing")
		return
	}

	if err = c.finalizeIntermediateBlock(c.ctx, cm.Certificate); err != nil {
		log.
			WithError(err).
			WithField("height", c.highestSeen).
			Error("could not accept intermediate block")
		return
	}

	// Set new intermediate block
	c.intermediateBlock = cm.Block

	// Notify mempool
	msg := message.New(topics.IntermediateBlock, *cm.Block)
	c.eventBus.Publish(topics.IntermediateBlock, msg)

	// propagate round update
	go func() {
		err = c.sendRoundUpdate()
		if err != nil {
			log.
				WithError(err).
				WithField("height", c.highestSeen).
				Error("could not sendRoundUpdate")
		}
	}()
}

func (c *Chain) finalizeIntermediateBlock(ctx context.Context, cert *block.Certificate) error {
	c.intermediateBlock.Header.Certificate = cert
	return c.AcceptBlock(ctx, *c.intermediateBlock)
}

// Send out a query for agreement messages and an intermediate block.
func (c *Chain) requestRoundResults(round uint64) (*block.Block, *block.Certificate, error) {
	roundResultsChan := make(chan message.Message, 10)
	id := c.eventBus.Subscribe(topics.RoundResults, eventbus.NewChanListener(roundResultsChan))
	defer c.eventBus.Unsubscribe(topics.RoundResults, id)

	buf := new(bytes.Buffer)
	if err := encoding.WriteUint64LE(buf, round); err != nil {
		//TODO: shall this really panic ?
		log.Panic(err)
	}

	// TODO: prepending the topic should be done at the recipient end of the
	// Gossip (together with all the other encoding)
	if err := topics.Prepend(buf, topics.GetRoundResults); err != nil {
		//TODO: shall this really panic ?
		log.Panic(err)
	}
	msg := message.New(topics.GetRoundResults, *buf)
	c.eventBus.Publish(topics.Gossip, msg)
	// We wait 5 seconds for a response. We time out otherwise and
	// attempt catching up later.
	timer := time.NewTimer(5 * time.Second)

	for {
		select {
		case <-timer.C:
			return nil, nil, errors.New("request timeout")
		case m := <-roundResultsChan:
			cm := m.Payload().(message.Candidate)

			// Check block and certificate for correctness
			if err := c.verifier.SanityCheckBlock(c.prevBlock, *cm.Block); err != nil {
				continue
			}

			calls, err := c.executor.ValidateStateTransition(c.ctx, c.prevBlock.Txs, c.prevBlock.Header.Height)
			if err != nil {
				continue
			}

			if len(calls) != len(c.prevBlock.Txs) {
				continue
			}

			// Certificate needs to be on a block to be verified.
			// Since this certificate is supposed to be for the
			// intermediate block, we can just put it on there.
			cm.Block.Header.Certificate = cm.Certificate
			if err := verifiers.CheckBlockCertificate(*c.p, *cm.Block); err != nil {
				continue
			}

			return cm.Block, cm.Certificate, nil
		}
	}
}

func (c *Chain) provideLastBlock(r rpcbus.Request) {
	c.mu.RLock()
	prevBlock := c.prevBlock
	c.mu.RUnlock()
	r.RespChan <- rpcbus.NewResponse(prevBlock, nil)
}

func (c *Chain) provideLastCertificate(r rpcbus.Request) {
	if c.lastCertificate == nil {
		r.RespChan <- rpcbus.NewResponse(bytes.Buffer{}, errors.New("no last certificate present"))
		return
	}

	buf := new(bytes.Buffer)
	err := message.MarshalCertificate(buf, c.lastCertificate)
	r.RespChan <- rpcbus.NewResponse(*buf, err)
}

func (c *Chain) provideLastCommittee(r rpcbus.Request) {
	if c.lastCommittee == nil {
		r.RespChan <- rpcbus.NewResponse(bytes.Buffer{}, errors.New("no last committee present"))
		return
	}

	r.RespChan <- rpcbus.NewResponse(c.lastCommittee, nil)
}

func (c *Chain) provideRoundResults(r rpcbus.Request) {
	if c.intermediateBlock == nil || c.lastCertificate == nil {
		r.RespChan <- rpcbus.NewResponse(bytes.Buffer{}, errors.New("no intermediate block or certificate currently known"))
		return
	}
	params := r.Params.(bytes.Buffer)

	if params.Len() < 8 {
		r.RespChan <- rpcbus.NewResponse(bytes.Buffer{}, errors.New("round cannot be read from request param"))
		return
	}

	round := binary.LittleEndian.Uint64(params.Bytes())
	if round != c.intermediateBlock.Header.Height {
		r.RespChan <- rpcbus.NewResponse(bytes.Buffer{}, errors.New("no intermediate block and certificate for the given round"))
		return
	}

	buf := new(bytes.Buffer)
	if err := message.MarshalBlock(buf, c.intermediateBlock); err != nil {
		r.RespChan <- rpcbus.NewResponse(bytes.Buffer{}, err)
		return
	}

	if err := message.MarshalCertificate(buf, c.lastCertificate); err != nil {
		r.RespChan <- rpcbus.NewResponse(bytes.Buffer{}, err)
		return
	}

	r.RespChan <- rpcbus.NewResponse(*buf, nil)
}

// GetSyncProgress returns how close the node is to being synced to the tip,
// as a percentage value.
func (c *Chain) GetSyncProgress(ctx context.Context, e *node.EmptyRequest) (*node.SyncProgressResponse, error) {
	if c.highestSeen == 0 {
		return &node.SyncProgressResponse{Progress: 0}, nil
	}

	c.mu.RLock()
	prevBlockHeight := c.prevBlock.Header.Height
	c.mu.RUnlock()

	progressPercentage := (float64(prevBlockHeight) / float64(c.highestSeen)) * 100

	// Avoiding strange output when the chain can be ahead of the highest
	// seen block, as in most cases, consensus terminates before we see
	// the new block from other peers.
	if progressPercentage > 100 {
		progressPercentage = 100
	}

	return &node.SyncProgressResponse{Progress: float32(progressPercentage)}, nil
}

// RebuildChain will delete all blocks except for the genesis block,
// to allow for a full re-sync.
func (c *Chain) RebuildChain(ctx context.Context, e *node.EmptyRequest) (*node.GenericResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Halt consensus
	msg := message.New(topics.StopConsensus, nil)
	c.eventBus.Publish(topics.StopConsensus, msg)

	// Remove EVERYTHING from the database. This includes the genesis
	// block, so we need to add it afterwards.
	if err := c.loader.Clear(); err != nil {
		return nil, err
	}

	// Note that, beyond this point, an error in reconstructing our
	// state is unrecoverable, as it deems the node totally useless.
	// Therefore, any error encountered from now on is answered by
	// a panic.
	var tipErr error
	var tip *block.Block
	tip, tipErr = c.loader.LoadTip()
	if tipErr != nil {
		log.Panic(tipErr)
	}

	c.prevBlock = *tip
	if unrecoverable := c.verifier.PerformSanityCheck(0, SanityCheckHeight, 0); unrecoverable != nil {
		log.Panic(unrecoverable)
	}

	// Reset in-memory values
	if err := c.resetState(); err != nil {
		log.Panic(err)
	}

	// Clear walletDB
	if _, err := c.rpcBus.Call(topics.ClearWalletDatabase, rpcbus.NewRequest(bytes.Buffer{}), 0*time.Second); err != nil {
		log.Panic(err)
	}

	return &node.GenericResponse{Response: "Blockchain deleted. Syncing from scratch..."}, nil
}

func (c *Chain) resetState() error {
	c.p = user.NewProvisioners()
	intermediateBlock, err := mockFirstIntermediateBlock(c.prevBlock.Header)
	if err != nil {
		return err
	}
	c.intermediateBlock = intermediateBlock

	c.lastCertificate = block.EmptyCertificate()
	return nil
}
