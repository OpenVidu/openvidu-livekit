package livekithelperinterface

import "github.com/livekit/protocol/livekit"

// This interface in a separate package fixes import cycles
type LivekitHelper interface {
	ListActiveRooms() ([]*livekit.Room, error)
	ListActiveParticipants() ([]*livekit.ParticipantInfo, error)
	ListActiveEgresses() ([]*livekit.EgressInfo, error)
	ListActiveIngresses() ([]*livekit.IngressInfo, error)
}
