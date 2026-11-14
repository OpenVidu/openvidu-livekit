// BEGIN OPENVIDU BLOCK
package rtc

import (
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/pkg/rtc/types"
)

// A full reconnection replaces a participant's session. Upstream then starts a
// second Auto Participant Egress while the first one keeps recording them.
type openviduAutoEgress struct {
	// identities whose session was replaced while their egress goes on
	replaced map[livekit.ParticipantIdentity]bool
}

// openviduOnParticipantRemoved marks a replaced session whose Auto Participant
// Egress goes on. Called with r.lock held, before hasPublished is cleared.
func (r *Room) openviduOnParticipantRemoved(identity livekit.ParticipantIdentity, reason types.ParticipantCloseReason) {
	if reason == types.ParticipantCloseReasonDuplicateIdentity &&
		(r.hasPublished[identity] || r.openviduAutoEgress.replaced[identity]) {
		if r.openviduAutoEgress.replaced == nil {
			r.openviduAutoEgress.replaced = make(map[livekit.ParticipantIdentity]bool)
		}
		r.openviduAutoEgress.replaced[identity] = true
		return
	}
	delete(r.openviduAutoEgress.replaced, identity)
}

// openviduStartsAutoEgress reports whether a session's first track starts an
// Auto Participant Egress: not when it replaced a session whose egress goes on.
func (r *Room) openviduStartsAutoEgress(identity livekit.ParticipantIdentity) bool {
	r.lock.Lock()
	defer r.lock.Unlock()
	if r.openviduAutoEgress.replaced[identity] {
		delete(r.openviduAutoEgress.replaced, identity)
		return false
	}
	return true
}

// END OPENVIDU BLOCK
