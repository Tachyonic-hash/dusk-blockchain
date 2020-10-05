package secondstep

import (
	"bytes"
	"context"
	"time"

	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/header"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/reduction"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/message"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/topics"
	log "github.com/sirupsen/logrus"
)

var lg = log.WithField("process", "secondstep reduction")

// Phase is the implementation of the Selection step component
type Phase struct {
	*reduction.Reduction
	handler    *reduction.Handler
	aggregator *reduction.Aggregator

	firstStepVotesMsg message.StepVotesMsg

	next consensus.Phase
}

// New creates and launches the component which responsibility is to reduce the
// candidates gathered as winner of the selection of all nodes in the committee
// and reduce them to just one candidate obtaining 64% of the committee vote
func New(e *consensus.Emitter, timeOut time.Duration) *Phase {
	return &Phase{
		Reduction: &reduction.Reduction{Emitter: e, TimeOut: timeOut},
	}
}

// SetNext sets the next step to be returned at the end of this one
func (p *Phase) SetNext(next consensus.Phase) {
	p.next = next
}

// Fn passes to this reduction step the best score collected during selection
func (p *Phase) Fn(re consensus.InternalPacket) consensus.PhaseFn {
	p.firstStepVotesMsg = re.(message.StepVotesMsg)
	return p.Run
}

// Run the first reduction step until either there is a timeout, we reach 64%
// of votes, or we experience an unrecoverable error
func (p *Phase) Run(ctx context.Context, queue *consensus.Queue, evChan chan message.Message, r consensus.RoundUpdate, step uint8) (consensus.PhaseFn, error) {
	lg.
		WithField("round", r.Round).
		WithField("step", step).
		Trace("starting secondstep reduction")
	p.handler = reduction.NewHandler(p.Keys, r.P)
	// first we send our own Selection
	if p.handler.AmMember(r.Round, step) {
		if err := p.SendReduction(r.Round, step, p.firstStepVotesMsg.BlockHash); err != nil {
			// in case of error we need to tell the consensus loop as we cannot
			// really recover from here
			return nil, err
		}
	}

	timeoutChan := time.After(p.TimeOut)
	p.aggregator = reduction.NewAggregator(p.handler)
	for _, ev := range queue.GetEvents(r.Round, step) {
		if ev.Category() == topics.Reduction {
			rMsg := ev.Payload().(message.Reduction)
			if !p.handler.IsMember(rMsg.Sender(), r.Round, step) {
				continue
			}
			// if collectReduction returns a StepVote, it means we reached
			// consensus and can go to the next step
			svm, err := p.collectReduction(rMsg, r.Round, step)
			if err != nil {
				return nil, err
			}

			if svm == nil {
				continue
			}

			if stepVotesAreValid(&p.firstStepVotesMsg, svm) && p.handler.AmMember(r.Round, step) {
				if err := p.sendAgreement(r.Round, step, svm); err != nil {
					return nil, err
				}
			}
			return p.next.Fn(nil), nil
		}
	}

	for {
		select {
		case ev := <-evChan:
			if reduction.ShouldProcess(ev, r.Round, step, queue) {
				rMsg := ev.Payload().(message.Reduction)
				if !p.handler.IsMember(rMsg.Sender(), r.Round, step) {
					continue
				}
				svm, err := p.collectReduction(rMsg, r.Round, step)
				if err != nil {
					return nil, err
				}

				if svm == nil {
					continue
				}

				go func() { // preventing timeout leakage
					<-timeoutChan
				}()

				if stepVotesAreValid(&p.firstStepVotesMsg, svm) && p.handler.AmMember(r.Round, step) {
					if err := p.sendAgreement(r.Round, step, svm); err != nil {
						return nil, err
					}
				}
				return p.next.Fn(nil), nil
			}

		case <-timeoutChan:
			// in case of timeout we increase the timeout and that's it
			p.IncreaseTimeout(r.Round)
			return p.next.Fn(nil), nil

		case <-ctx.Done():
			// preventing timeout leakage
			go func() {
				<-timeoutChan
			}()
			return nil, nil
		}
	}
}

func (p *Phase) collectReduction(r message.Reduction, round uint64, step uint8) (*message.StepVotesMsg, error) {
	if err := p.handler.VerifySignature(r); err != nil {
		return nil, err
	}

	hdr := r.State()
	lg.WithFields(log.Fields{
		"round": hdr.Round,
		"step":  hdr.Step,
		//"sender": hex.EncodeToString(hdr.Sender()),
		//"hash":   hex.EncodeToString(hdr.BlockHash),
	}).Debugln("received_2nd_step_reduction")
	result, err := p.aggregator.CollectVote(r)
	if err != nil {
		return nil, err
	}

	return p.createStepVoteMessage(result, round, step), nil
}

func (p *Phase) createStepVoteMessage(r *reduction.Result, round uint64, step uint8) *message.StepVotesMsg {
	if r == nil {
		return nil
	}

	// quorum has been reached. However hash&votes can be empty
	return &message.StepVotesMsg{
		Header: header.Header{
			Step:      step,
			Round:     round,
			BlockHash: r.Hash,
			PubKeyBLS: p.Keys.BLSPubKeyBytes,
		},
		StepVotes: r.SV,
	}
}

func (p *Phase) sendAgreement(round uint64, step uint8, svm *message.StepVotesMsg) error {
	hdr := header.Header{
		Round:     round,
		Step:      step,
		PubKeyBLS: p.Keys.BLSPubKeyBytes,
		BlockHash: svm.BlockHash,
	}

	sig, err := p.Sign(hdr)
	if err != nil {
		return err
	}

	// then we create the full BLS signed Agreement
	// XXX: the StepVotes are NOT signed (i.e. the message.SignAgreement is not used).
	// This exposes the Agreement to some malleability attack. Double check
	// this!!
	ev := message.NewAgreement(hdr)
	ev.SetSignature(sig)
	ev.VotesPerStep = []*message.StepVotes{
		&p.firstStepVotesMsg.StepVotes,
		&svm.StepVotes,
	}

	return p.Gossip(message.New(topics.Agreement, *ev))
}

func stepVotesAreValid(svs ...*message.StepVotesMsg) bool {
	return len(svs) == 2 &&
		!svs[0].IsEmpty() &&
		!svs[1].IsEmpty() &&
		!bytes.Equal(svs[0].BlockHash, reduction.EmptyHash[:]) &&
		!bytes.Equal(svs[1].BlockHash, reduction.EmptyHash[:])
}
