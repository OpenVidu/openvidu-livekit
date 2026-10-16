// Copyright 2026 OpenVidu
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package projection

import (
	"github.com/livekit/protocol/livekit"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// raw builds the raw event document when the event type is stored. INGRESS_UPDATED is stored only when the state
// status changed since the last event of the ingress the batch saw; with no previous status it is stored.
func (p *projector) raw() {
	eventType := p.event.GetType().String()
	if p.opts.Stores == nil || !p.opts.Stores(eventType) || p.event.GetId() == "" {
		return
	}
	if p.event.GetType() == livekit.AnalyticsEventType_INGRESS_UPDATED {
		ingressID := firstNonEmpty(p.event.GetIngressId(), p.event.GetIngress().GetIngressId())
		previous := p.refs.IngressStatus[ingressID]
		if previous != "" && previous == p.event.GetIngress().GetState().GetStatus().String() {
			return
		}
	}

	roomSid, roomName := roomRef(p.event)
	participantSid := firstNonEmpty(p.event.GetParticipantId(), p.event.GetParticipant().GetSid())
	payload := schema.Encode(p.event, p.opts.Encode)
	if payload == nil {
		payload = bson.D{}
	}
	p.result.Raw = &schema.EventDocument{
		ID:               p.event.GetId(),
		Type:             eventType,
		TS:               p.ts,
		RoomSID:          roomSid,
		RoomName:         roomName,
		ParticipantSID:   participantSid,
		Identity:         firstNonEmpty(p.event.GetParticipant().GetIdentity(), p.refs.Identities[participantSid]),
		TrackSID:         firstNonEmpty(p.event.GetTrackId(), p.event.GetTrack().GetSid()),
		EgressID:         firstNonEmpty(p.event.GetEgressId(), p.event.GetEgress().GetEgressId()),
		IngressID:        firstNonEmpty(p.event.GetIngressId(), p.event.GetIngress().GetIngressId()),
		IngressSessionID: p.event.GetIngress().GetState().GetResourceId(),
		NodeID:           p.event.GetNodeId(),
		Payload:          payload,
	}
}
