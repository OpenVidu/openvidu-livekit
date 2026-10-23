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
	f.set("lastEventAt", maxDate("lastEventAt", p.ts))
	f.set("schemaVersion", int32(schema.SchemaVersion))
	return f
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
