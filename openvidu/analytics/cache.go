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

package analytics

import (
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
)

// Cache sizes bound the memory of the references a node keeps about the sessions it saw.
const (
	identityCacheSize = 50_000
	trackCacheSize    = 100_000
	ingressCacheSize  = 10_000
)

// refCache remembers, per node, what track and subscription events do not carry: participant identities, track
// publishers and sources, the tracks of each publisher, and the last state of each ingress.
type refCache struct {
	identities        *lru.Cache[string, string]
	tracks            *lru.Cache[string, projection.TrackRef]
	participantTracks *lru.Cache[string, []string]
	ingressStatus     *lru.Cache[string, string]
}

func newRefCache() *refCache {
	identities, _ := lru.New[string, string](identityCacheSize)
	tracks, _ := lru.New[string, projection.TrackRef](trackCacheSize)
	participantTracks, _ := lru.New[string, []string](identityCacheSize)
	ingressStatus, _ := lru.New[string, string](ingressCacheSize)
	return &refCache{identities: identities, tracks: tracks, participantTracks: participantTracks, ingressStatus: ingressStatus}
}

// refsFor returns the references the projection of event needs, as known before the event.
func (c *refCache) refsFor(event *livekit.AnalyticsEvent) projection.Refs {
	refs := projection.Refs{
		Identities:        map[string]string{},
		Tracks:            map[string]projection.TrackRef{},
		ParticipantTracks: map[string][]string{},
		IngressStatus:     map[string]string{},
	}
	participantSid := firstNonEmpty(event.GetParticipantId(), event.GetParticipant().GetSid())
	if identity, ok := c.identities.Get(participantSid); ok {
		refs.Identities[participantSid] = identity
	}
	trackSid := firstNonEmpty(event.GetTrackId(), event.GetTrack().GetSid())
	if track, ok := c.tracks.Get(trackSid); ok {
		refs.Tracks[trackSid] = track
		if identity, ok := c.identities.Get(track.ParticipantSID); ok {
			refs.Identities[track.ParticipantSID] = identity
		}
	}
	if tracks, ok := c.participantTracks.Get(participantSid); ok {
		refs.ParticipantTracks[participantSid] = tracks
	}
	ingressID := firstNonEmpty(event.GetIngressId(), event.GetIngress().GetIngressId())
	if status, ok := c.ingressStatus.Get(ingressID); ok {
		refs.IngressStatus[ingressID] = status
	}
	return refs
}

// learn records what event tells about participants, tracks and ingresses.
func (c *refCache) learn(event *livekit.AnalyticsEvent) {
	if participant := event.GetParticipant(); participant.GetSid() != "" && participant.GetIdentity() != "" {
		c.identities.Add(participant.GetSid(), participant.GetIdentity())
	}
	if publisher := event.GetPublisher(); publisher.GetSid() != "" && publisher.GetIdentity() != "" {
		c.identities.Add(publisher.GetSid(), publisher.GetIdentity())
	}

	switch event.GetType() {
	case livekit.AnalyticsEventType_TRACK_PUBLISHED, livekit.AnalyticsEventType_TRACK_SUBSCRIBED:
		track := event.GetTrack()
		publisherSid := event.GetParticipant().GetSid()
		if event.GetType() == livekit.AnalyticsEventType_TRACK_SUBSCRIBED {
			publisherSid = event.GetPublisher().GetSid()
		}
		if track.GetSid() != "" && publisherSid != "" {
			c.tracks.Add(track.GetSid(), projection.TrackRef{
				ParticipantSID: publisherSid, Source: track.GetSource().String(), Type: track.GetType().String(),
			})
			c.addParticipantTrack(publisherSid, track.GetSid())
		}
	case livekit.AnalyticsEventType_INGRESS_CREATED, livekit.AnalyticsEventType_INGRESS_STARTED,
		livekit.AnalyticsEventType_INGRESS_UPDATED, livekit.AnalyticsEventType_INGRESS_ENDED,
		livekit.AnalyticsEventType_INGRESS_DELETED:
		ingress := event.GetIngress()
		if id := firstNonEmpty(event.GetIngressId(), ingress.GetIngressId()); id != "" {
			c.ingressStatus.Add(id, ingress.GetState().GetStatus().String())
		}
	}
}

func (c *refCache) addParticipantTrack(participantSid string, trackSid string) {
	tracks, _ := c.participantTracks.Get(participantSid)
	for _, existing := range tracks {
		if existing == trackSid {
			return
		}
	}
	c.participantTracks.Add(participantSid, append(tracks, trackSid))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
