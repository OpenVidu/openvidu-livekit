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

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

const fiveMinutes = 5 * time.Minute

// Scopes of a rollup increment: the whole deployment and the node that emitted the event.
func scopes(nodeID string) []string {
	if nodeID == "" {
		return []string{"global"}
	}
	return []string{"global", "node:" + nodeID}
}

// counters adds the same counters to the hourly and daily documents of both scopes.
func (p *projector) counters(counters map[string]any) {
	p.result.Increments = append(p.result.Increments, periodIncrements(p.event.GetNodeId(), p.ts, counters)...)
}

// fiveMinute adds counters to the 5-minute documents of both scopes.
func (p *projector) fiveMinute(counters map[string]any) {
	p.result.Increments = append(p.result.Increments, fiveMinuteIncrements(p.event.GetNodeId(), p.ts, counters)...)
}

func fiveMinuteIncrements(nodeID string, at time.Time, counters map[string]any) []Increment {
	var increments []Increment
	for _, scope := range scopes(nodeID) {
		increments = append(increments, Increment{
			Collection: schema.CollectionAnalytics5Min, Scope: scope, Bucket: at.Truncate(fiveMinutes), Counters: counters,
		})
	}
	return increments
}

func periodIncrements(nodeID string, at time.Time, counters map[string]any) []Increment {
	var increments []Increment
	for _, scope := range scopes(nodeID) {
		increments = append(increments,
			Increment{Collection: schema.CollectionAnalyticsHourly, Scope: scope, Bucket: at.Truncate(time.Hour), Counters: counters},
			Increment{Collection: schema.CollectionAnalyticsDaily, Scope: scope, Bucket: at.Truncate(24 * time.Hour), Counters: counters},
		)
	}
	return increments
}

// RoomEndedIncrements returns the participantsSum increments of a room closed at endedAt on nodeID, once the batch
// knows its STANDARD participant sessions.
func RoomEndedIncrements(nodeID string, endedAt time.Time, participants int32) []Increment {
	return periodIncrements(nodeID, endedAt, map[string]any{"rooms.participantsSum": participants})
}

var one = int32(1)

func (p *projector) rollups() {
	if id := ClosedEntity(p.event); id != "" && p.refs.Reconciled[id] {
		// The reconciler counted this closure; the real event only corrects the closure fields.
		p.result.EndedRoom = ""
		return
	}
	switch p.event.GetType() {
	case livekit.AnalyticsEventType_ROOM_CREATED:
		p.counters(map[string]any{"rooms.created": one})
		p.fiveMinute(map[string]any{"rooms.created": one})
	case livekit.AnalyticsEventType_ROOM_ENDED:
		reason := enumName(p.event.GetRoomEndReason().String(), "ROOM_END_")
		counters := map[string]any{"rooms.ended": one, "rooms.endedByReason." + reason: one}
		if created := roomCreationTime(p.event.GetRoom()); !created.IsZero() {
			counters["rooms.durationMsSum"] = schema.DurationMs(created, p.ts)
		}
		p.counters(counters)
		p.fiveMinute(map[string]any{"rooms.ended": one})
	case livekit.AnalyticsEventType_PARTICIPANT_JOINED:
		info := p.event.GetParticipant()
		counters := map[string]any{"participants.byKind." + info.GetKind().String(): one}
		if info.GetKind() == livekit.ParticipantInfo_STANDARD {
			client := p.event.GetClientInfo()
			counters["participants.joined"] = one
			counters["participants.bySdk."+client.GetSdk().String()] = one
			counters["participants.byOs."+osFamily(client.GetOs())] = one
			counters["participants.byBrowser."+browserFamily(client.GetBrowser())] = one
			p.fiveMinute(map[string]any{"participants.joined": one})
		}
		p.counters(counters)
	case livekit.AnalyticsEventType_PARTICIPANT_ACTIVE:
		if p.event.GetParticipant().GetKind() != livekit.ParticipantInfo_STANDARD {
			return
		}
		meta := p.event.GetClientMeta()
		counters := map[string]any{
			"participants.active": one,
			"participants.byConnectionType." + connectionTypeKey(meta.GetConnectionType()): one,
		}
		if meta.GetClientConnectTime() > 0 {
			counters["participants.connectTimeSumMs"] = int64(meta.GetClientConnectTime())
			counters["participants.connectTimeCount"] = one
		}
		p.counters(counters)
		p.fiveMinute(map[string]any{"participants.active": one})
	case livekit.AnalyticsEventType_PARTICIPANT_LEFT, livekit.AnalyticsEventType_PARTICIPANT_CONNECTION_ABORTED:
		info := p.event.GetParticipant()
		if info.GetKind() != livekit.ParticipantInfo_STANDARD {
			return
		}
		counter := "participants.left"
		if p.event.GetType() == livekit.AnalyticsEventType_PARTICIPANT_CONNECTION_ABORTED {
			counter = "participants.aborted"
		}
		counters := map[string]any{counter: one, "participants.disconnectReasons." + info.GetDisconnectReason().String(): one}
		if counter == "participants.left" {
			if joined := joinTime(info, time.Time{}); !joined.IsZero() {
				counters["participants.minutes"] = float64(schema.DurationMs(joined, p.ts)) / 60000
			}
		}
		p.counters(counters)
		p.fiveMinute(map[string]any{counter: one})
	case livekit.AnalyticsEventType_TRACK_PUBLISHED:
		p.counters(map[string]any{"tracks.published": one})
	case livekit.AnalyticsEventType_TRACK_SUBSCRIBED:
		p.counters(map[string]any{"tracks.subscribed": one})
	case livekit.AnalyticsEventType_TRACK_SUBSCRIBE_FAILED:
		p.counters(map[string]any{"tracks.subscribeFailed": one})
	case livekit.AnalyticsEventType_EGRESS_STARTED:
		p.counters(map[string]any{"egress.started": one, "egress.byType." + egressType(p.event.GetEgress()): one})
		p.fiveMinute(map[string]any{"egress.started": one})
	case livekit.AnalyticsEventType_EGRESS_ENDED:
		info := p.event.GetEgress()
		status := strings.ToLower(enumName(info.GetStatus().String(), "EGRESS_"))
		counters := map[string]any{"egress.endedByStatus." + status: one}
		if info.GetStartedAt() != 0 {
			ended := orTime(schema.UnixNanos(info.GetEndedAt()), p.ts)
			counters["egress.durationMs"] = schema.DurationMs(schema.UnixNanos(info.GetStartedAt()), ended)
		}
		p.counters(counters)
		p.fiveMinute(map[string]any{"egress.ended": one})
	case livekit.AnalyticsEventType_INGRESS_STARTED:
		p.counters(map[string]any{"ingress.sessionsStarted": one})
	case livekit.AnalyticsEventType_INGRESS_ENDED:
		state := p.event.GetIngress().GetState()
		counters := map[string]any{"ingress.sessionsEnded": one}
		if state.GetStartedAt() != 0 {
			ended := orTime(schema.UnixNanos(state.GetEndedAt()), p.ts)
			counters["ingress.durationMs"] = schema.DurationMs(schema.UnixNanos(state.GetStartedAt()), ended)
		}
		p.counters(counters)
	}
}

// apiCall counts an API call by Twirp method in the hourly and daily documents; API calls are never stored raw.
func (p *projector) apiCall() {
	call := p.event.GetApiCall()
	method := apiMethodKey(call.GetService(), call.GetMethod())
	counters := map[string]any{"apiCalls.total": one, "apiCalls.byMethod." + method: one}
	if call.GetTwirpErrorCode() != "" || call.GetStatus() >= 400 {
		counters["apiErrors.total"] = one
		counters["apiErrors.byMethod."+method] = one
	}
	p.counters(counters)
}

// apiMethodKey builds the `<Service>_<Method>` key of the API counters from server-defined names.
func apiMethodKey(service string, method string) string {
	service = alphanumeric(service[strings.LastIndex(service, ".")+1:])
	method = alphanumeric(method)
	if service == "" {
		service = "Unknown"
	}
	if method == "" {
		method = "Unknown"
	}
	return service + "_" + method
}

func alphanumeric(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
