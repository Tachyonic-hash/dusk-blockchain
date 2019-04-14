package agreement

import (
	"bytes"

	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/committee"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/events"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire/encoding"
)

type agreementHandler struct {
	committee.Committee
	*events.AgreementUnMarshaller
}

func newHandler(committee committee.Committee) *agreementHandler {
	return &agreementHandler{
		Committee:             committee,
		AgreementUnMarshaller: events.NewAgreementUnMarshaller(),
	}
}

func (a *agreementHandler) NewEvent() wire.Event {
	return events.NewAgreement()
}

func (a *agreementHandler) ExtractHeader(e wire.Event, h *events.Header) {
	ev := e.(*events.Agreement)
	h.Round = ev.Round
	h.Step = ev.Step
}

func (a *agreementHandler) ExtractIdentifier(e wire.Event, r *bytes.Buffer) error {
	ev := e.(*events.Agreement)
	return encoding.WriteUint8(r, ev.Step)
}

func (a *agreementHandler) Verify(e wire.Event) error {
	ev := e.(*events.Agreement)
	return a.Committee.VerifyVoteSet(ev.VoteSet, ev.AgreedHash, ev.Round, ev.Step)
}
