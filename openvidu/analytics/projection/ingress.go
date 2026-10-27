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

	"github.com/livekit/protocol/livekit"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

const ingressDeleted = "deleted"

// Session statuses that end a publishing session.
var ingressTerminal = bson.A{"inactive", "error", "complete"}

func (p *projector) ingress() {
	info := p.event.GetIngress()
	id := firstNonEmpty(p.event.GetIngressId(), info.GetIngressId())
	if id == "" {
		return
	}
	state := info.GetState()
	stateStatus := ingressStateStatus(state.GetStatus())
	stateAt := orTime(schema.UnixNanos(state.GetUpdatedAt()), p.ts)
	inputType := ingressInputType(info.GetInputType())
	resourceID := state.GetResourceId()
	eventType := p.event.GetType()
	session := resourceID != "" && eventType != livekit.AnalyticsEventType_INGRESS_DELETED &&
		eventType != livekit.AnalyticsEventType_INGRESS_CREATED
	terminal := containsAny(ingressTerminal, stateStatus)

	f := newFields()
	if info.GetName() != "" {
		f.set("name", info.GetName())
		f.set("nameLower", strings.ToLower(info.GetName()))
	}
	f.set("inputType", inputType)
	if info.GetUrl() != "" {
		f.set("url", schema.StripURLCredentials(info.GetUrl()))
	}
	f.set("roomName", info.GetRoomName())
	f.set("participantIdentity", info.GetParticipantIdentity())
	if info.GetParticipantName() != "" {
		f.set("participantName", info.GetParticipantName())
	}
	if p.opts.Encode.StoreMetadata && info.GetParticipantMetadata() != "" {
		f.set("participantMetadata", info.GetParticipantMetadata())
	}
	f.set("reusable", info.GetReusable())
	f.set("enabled", info.Enabled == nil || info.GetEnabled())
	if info.EnableTranscoding != nil {
		f.set("enableTranscoding", info.GetEnableTranscoding())
	}
	f.set("createdAt", minDate("createdAt", p.ts))
	isDeleted := eq(field("status"), ingressDeleted)
	if eventType == livekit.AnalyticsEventType_INGRESS_DELETED {
		f.set("status", ingressDeleted)
		f.set("deletedAt", keep("deletedAt", p.ts))
	} else {
		f.set("status", cond(isDeleted, ingressDeleted, latest("status", "internal.stateAt", stateAt, stateStatus)))
		f.set("internal.stateAt", maxDate("internal.stateAt", stateAt))
	}
	if state.GetError() != "" {
		f.set("lastError", state.GetError())
	}
	if session {
		started := orTime(schema.UnixNanos(state.GetStartedAt()), p.ts)
		f.set("lastSessionAt", maxDate("lastSessionAt", started))
		f.set("internal.sessionIds", union("internal.sessionIds", resourceID))
		if terminal && state.GetEndedAt() != 0 && state.GetStartedAt() != 0 {
			duration := schema.DurationMs(schema.UnixNanos(state.GetStartedAt()), schema.UnixNanos(state.GetEndedAt()))
			f.set("internal.endedSessionDurations."+resourceID, duration)
		}
	}
	f.set("schemaVersion", int32(schema.SchemaVersion))
	p.upsert(schema.CollectionIngresses, id, f)

	if !session {
		return
	}
	s := newFields()
	s.set("ingressId", id)
	if info.GetName() != "" {
		s.set("ingressName", info.GetName())
	}
	s.set("inputType", inputType)
	if state.GetRoomId() != "" {
		s.set("roomSid", keep("roomSid", state.GetRoomId()))
	}
	if info.GetRoomName() != "" {
		s.set("roomName", keep("roomName", info.GetRoomName()))
	}
	if info.GetParticipantIdentity() != "" {
		s.set("participantIdentity", info.GetParticipantIdentity())
	}
	sessionTerminal := in(field("status"), ingressTerminal)
	s.set("status", cond(sessionTerminal, field("status"), stateStatus))
	if state.GetError() != "" {
		s.set("error", state.GetError())
	}
	s.set("startedAt", minDate("startedAt", orTime(schema.UnixNanos(state.GetStartedAt()), p.ts)))
	if terminal {
		ended := orTime(schema.UnixNanos(state.GetEndedAt()), p.ts)
		s.set("endedAt", cond(sessionTerminal, field("endedAt"), ended))
		s.set("closedBy", byEvent)
	}
	if video := state.GetVideo(); video != nil {
		s.set("video", videoDocument(video))
	}
	if audio := state.GetAudio(); audio != nil {
		s.set("audio", audioDocument(audio))
	}
	s.set("schemaVersion", int32(schema.SchemaVersion))
	p.upsert(schema.CollectionIngressSessions, resourceID, s)

	if roomSid := state.GetRoomId(); roomSid != "" {
		room := p.roomTouch(info.GetRoomName())
		room.set("internal.ingressSessionIds", union("internal.ingressSessionIds", resourceID))
		if terminal {
			room.set("internal.endedIngressSessionIds", union("internal.endedIngressSessionIds", resourceID))
		}
		p.upsert(schema.CollectionRooms, roomSid, room)
	}
}

func containsAny(values bson.A, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func ingressStateStatus(status livekit.IngressState_Status) string {
	return strings.ToLower(enumName(status.String(), "ENDPOINT_"))
}

func ingressInputType(input livekit.IngressInput) string {
	return strings.ToLower(strings.TrimSuffix(input.String(), "_INPUT"))
}

func videoDocument(video *livekit.InputVideoState) bson.D {
	f := newFields()
	if video.GetMimeType() != "" {
		f.set("mimeType", video.GetMimeType())
	}
	if video.GetAverageBitrate() > 0 {
		f.set("averageBitrate", clampInt32(int64(video.GetAverageBitrate())))
	}
	if video.GetWidth() > 0 {
		f.set("width", clampInt32(int64(video.GetWidth())))
	}
	if video.GetHeight() > 0 {
		f.set("height", clampInt32(int64(video.GetHeight())))
	}
	if video.GetFramerate() > 0 {
		f.set("framerate", video.GetFramerate())
	}
	return f.doc()
}

func audioDocument(audio *livekit.InputAudioState) bson.D {
	f := newFields()
	if audio.GetMimeType() != "" {
		f.set("mimeType", audio.GetMimeType())
	}
	if audio.GetAverageBitrate() > 0 {
		f.set("averageBitrate", clampInt32(int64(audio.GetAverageBitrate())))
	}
	if audio.GetChannels() > 0 {
		f.set("channels", clampInt32(int64(audio.GetChannels())))
	}
	if audio.GetSampleRate() > 0 {
		f.set("sampleRate", clampInt32(int64(audio.GetSampleRate())))
	}
	return f.doc()
}

func deriveIngress(opts Options) *fields {
	retention, _, _ := opts.Retention.Of(schema.RetentionEntities)
	durations := bson.D{{Key: "$map", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$objectToArray", Value: ifNull("internal.endedSessionDurations", bson.D{})}}},
		{Key: "in", Value: ref("$$this.v")},
	}}}
	f := newFields()
	f.set("sessions", sizeOf("internal.sessionIds"))
	f.set("totalDurationMs", bson.D{{Key: "$toLong", Value: bson.D{{Key: "$sum", Value: durations}}}})
	f.set("expiresAt", cond(eq(field("status"), ingressDeleted), add(field("deletedAt"), retention.Milliseconds()), nil))
	return f
}

func deriveIngressSession(opts Options) *fields {
	retention, _, _ := opts.Retention.Of(schema.RetentionEntities)
	f := newFields()
	f.set("durationMs", cond(exists("endedAt"), subtract(field("endedAt"), field("startedAt")), remove))
	f.set("expiresAt", cond(in(field("status"), ingressTerminal), add(ifNull("endedAt", field("startedAt")),
		retention.Milliseconds()), nil))
	return f
}
