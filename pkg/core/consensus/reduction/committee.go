package reduction

import (
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/committee"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/user"
)

const committeeSize = 64

// Reducers defines a committee of reducers, and provides the ability to detect those
// who are not properly participating in this phase of the consensus.
type Reducers interface {
	committee.Committee
}

type reductionCommittee struct {
	*committee.Cache
	committees []user.VotingCommittee
}

func newReductionCommittee() *reductionCommittee {
	r := &reductionCommittee{
		Cache: committee.NewCache(),
	}
	return r
}

// IsMember checks if the BLS key belongs to one of the Provisioners in the committee
func (r *reductionCommittee) IsMember(provisioners user.Provisioners, pubKeyBLS []byte, round uint64, step uint8) bool {
	if int(step) > len(r.committees) {
		startingStep := uint8(len(r.committees))
		amount := step - startingStep + 8
		r.Cache.PregenerateCommittees(provisioners, round, startingStep, amount, r.size(provisioners))
	}
	votingCommittee := r.committees[step-1]
	return votingCommittee.IsMember(pubKeyBLS)
}

// Quorum returns the amount of votes to reach a quorum
func (r *reductionCommittee) Quorum(provisioners user.Provisioners) int {
	return int(float64(r.size(provisioners)) * 0.75)
}

func (r *reductionCommittee) size(provisioners user.Provisioners) int {
	size := len(provisioners.Members)
	if size > committeeSize {
		return committeeSize
	}

	return size
}
