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
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

const (
	roomActive = "active"
	roomClosed = "closed"
	byEvent    = "event"
)

var roomRanks = []rank{{roomActive, 0}, {roomClosed, 1}}

// roomTouch returns the fields every event of a room sets on it: the room is created with the facts the event carries
// when it does not exist yet (egress, ingress and track events can arrive before ROOM_CREATED is applied).
func (p *projector) roomTouch(name string) *fields {
	f := newFields()
	if name != "" {
		f.set("name", keep("name", name))
		f.set("nameLower", keep("nameLower", strings.ToLower(name)))
	}
	f.set("status", keep("status", roomActive))
	f.set("createdAt", minDate("createdAt", p.ts))
	f.set("nodeId", keep("nodeId", p.event.GetNodeId()))
	// Marks a room this writer version projects: only such a room gets a topology (deriveRoom).
	f.set("participants.peakPublishers", keep("participants.peakPublishers", int32(0)))
	p.markFirstWriter(f)
	f.set("lastEventAt", maxDate("lastEventAt", p.ts))
	f.set("schemaVersion", int32(schema.SchemaVersion))
	return f
}

// markFirstWriter stamps mediaStats: false, when this node reports no media statistics, and its media node label on an
// entity this node writes first: both marks follow nodeId, which the first writer sets.
func (p *projector) markFirstWriter(f *fields) {
	if p.opts.WithoutMediaStats {
		f.set("mediaStats", cond(exists("nodeId"), field("mediaStats"), false))
	}
	if p.opts.MediaNode != "" {
		f.set("mediaNode", cond(exists("nodeId"), field("mediaNode"), p.opts.MediaNode))
	}
}

// mediaNodeMark is the media node label of an entity this node hosts, none when the node has no label.
func (p *projector) mediaNodeMark() any {
	if p.opts.MediaNode != "" {
		return p.opts.MediaNode
	}
	return remove
}

// mediaStatsMark is the mark of an entity this node hosts: false when it reports no media statistics, none otherwise.
func (p *projector) mediaStatsMark() any {
	if p.opts.WithoutMediaStats {
		return false
	}
	return remove
}

func (p *projector) room() {
	room := p.event.GetRoom()
	sid := firstNonEmpty(p.event.GetRoomId(), room.GetSid())
	if sid == "" {
		return
	}

	f := p.roomTouch(room.GetName())
	if created := roomCreationTime(room); !created.IsZero() {
		f.set("createdAt", minDate("createdAt", created))
	}

	switch p.event.GetType() {
	case livekit.AnalyticsEventType_ROOM_CREATED:
		f.set("status", ranked("status", roomActive, roomRanks))
		f.set("nodeId", p.event.GetNodeId())
		f.set("mediaStats", p.mediaStatsMark())
		f.set("mediaNode", p.mediaNodeMark())
		f.set("config.maxParticipants", int32(room.GetMaxParticipants()))
		f.set("config.emptyTimeoutMs", schema.SecondsToMs(room.GetEmptyTimeout()))
		f.set("config.departureTimeoutMs", schema.SecondsToMs(room.GetDepartureTimeout()))
		if p.opts.Encode.StoreMetadata && room.GetMetadata() != "" {
			f.set("config.metadata", room.GetMetadata())
		}
	case livekit.AnalyticsEventType_ROOM_ENDED:
		reason := enumName(p.event.GetRoomEndReason().String(), "ROOM_END_")
		closedByEvent := eq(field("closedBy"), byEvent)
		f.set("status", roomClosed)
		f.set("endedAt", cond(closedByEvent, field("endedAt"), p.ts))
		f.set("endReason", cond(closedByEvent, field("endReason"), reason))
		f.set("closedBy", byEvent)
		// Nobody stays active in a closed room, even when the leave events of its participants are late or lost.
		f.set("internal.closedSids", bson.D{{Key: "$setUnion", Value: bson.A{
			emptyArray("internal.closedSids"), emptyArray("internal.activeSids")}}})
		p.result.EndedRoom = sid
	}
	p.upsert(schema.CollectionRooms, sid, f)
}

// roomCreationTime reads Room.creation_time_ms, falling back to creation_time in seconds.
func roomCreationTime(room *livekit.Room) time.Time {
	if room.GetCreationTimeMs() != 0 {
		return schema.UnixMillis(room.GetCreationTimeMs())
	}
	return schema.UnixSeconds(room.GetCreationTime())
}

// activeAfter is the number of active STANDARD participants once sid joins the active set.
func activeAfter(sid string) any {
	return size(difference(union("internal.activeSids", sid), emptyArray("internal.closedSids")))
}

// publishersAfter is the number of active STANDARD sessions that have sent media, from the given active and sender
// sets: the stored ones, or the stored ones with the session an event adds. The active set holds STANDARD sessions
// only, so agents and ingress, which send media too, never count.
func publishersAfter(active any, senders any) any {
	return size(difference(intersection(active, senders), emptyArray("internal.closedSids")))
}

// sentMedia records that session sid of a room sent media, and raises the room's publisher peak.
func sentMedia(room *fields, sid string) {
	room.set("internal.senderSids", union("internal.senderSids", sid))
	room.set("participants.peakPublishers", maxOf("participants.peakPublishers",
		publishersAfter(emptyArray("internal.activeSids"), union("internal.senderSids", sid))))
}

// topologyOf is the class of a room from its most STANDARD sessions active at once (N) and the most of them that
// have sent media at once (P).
func topologyOf(peakActive any, peakPublishers any) bson.D {
	branch := func(condition any, topology string) bson.D {
		return bson.D{{Key: "case", Value: condition}, {Key: "then", Value: topology}}
	}
	return bson.D{{Key: "$switch", Value: bson.D{
		{Key: "branches", Value: bson.A{
			branch(eq(peakActive, 0), TopologyEmpty),
			branch(eq(peakActive, 1), TopologySingleParticipant),
			branch(eq(peakPublishers, 0), TopologyNoPublishers),
			branch(eq(peakActive, 2), TopologyOneToOne),
			branch(eq(peakPublishers, 1), TopologyOneToMany),
		}},
		{Key: "default", Value: TopologyManyToMany},
	}}}
}

func deriveRoom(opts Options) *fields {
	retention, _, _ := opts.Retention.Of(schema.RetentionEntities)
	active := size(difference(emptyArray("internal.activeSids"), emptyArray("internal.closedSids")))
	egressTotal := sizeOf("internal.egressIds")
	ingressSessions := sizeOf("internal.ingressSessionIds")

	f := newFields()
	f.set("participants.sessions", sizeOf("internal.kinds."+StandardKind))
	f.set("participants.unique", sizeOf("internal.identities"))
	f.set("participants.active", active)
	f.set("participants.peakActive", maxOf("participants.peakActive", active))
	f.set("participants.aborted", sizeOf("internal.abortedSids"))
	f.set("participants.byKind", sizesOf("internal.kinds"))
	f.set("tracks.published", sizeOf("internal.trackSids"))
	f.set("tracks.bySource", sizesOf("internal.tracksBySource"))
	f.set("egress.total", egressTotal)
	f.set("egress.active", size(difference(emptyArray("internal.egressIds"), emptyArray("internal.endedEgressIds"))))
	f.set("egress.byType", sizesOf("internal.egressByType"))
	f.set("ingress.sessions", ingressSessions)
	f.set("ingress.active", size(difference(emptyArray("internal.ingressSessionIds"),
		emptyArray("internal.endedIngressSessionIds"))))
	f.set("features.egress", gt(egressTotal, 0))
	f.set("features.ingress", or(gt(ingressSessions, 0), gt(sizeOf("internal.kinds.INGRESS"), 0)))
	f.set("features.agent", gt(sizeOf("internal.kinds.AGENT"), 0))
	f.set("features.sip", gt(sizeOf("internal.kinds.SIP"), 0))
	// Only rooms this writer version projects carry the marker; a room of an earlier writer closed by the reconciler
	// gets neither a class nor a recording flag, rather than a wrong one.
	classified := exists("participants.peakPublishers")
	f.set("features.recording", cond(classified, gt(sizeOf("internal.recordingEgressIds"), 0), remove))
	f.set("topology", cond(classified,
		topologyOf(keep("participants.peakActive", int32(0)), keep("participants.peakPublishers", int32(0))), remove))
	f.set("bytes.up", keep("bytes.up", int64(0)))
	f.set("bytes.down", keep("bytes.down", int64(0)))
	setQualityDefaults(f)
	f.set("durationMs", cond(exists("endedAt"), subtract(field("endedAt"), field("createdAt")), remove))
	f.set("expiresAt", cond(eq(field("status"), roomClosed), add(field("endedAt"), retention.Milliseconds()), nil))
	return f
}

func setQualityDefaults(f *fields) {
	f.set("quality.scoreSum", keep("quality.scoreSum", float64(0)))
	f.set("quality.scoreCount", keep("quality.scoreCount", int32(0)))
	f.set("quality.poor", keep("quality.poor", int32(0)))
	f.set("quality.lost", keep("quality.lost", int32(0)))
}
