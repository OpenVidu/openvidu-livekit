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
	"maps"
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

const (
	fiveMinutes = 5 * time.Minute
	globalScope = "global"
)

// Scopes of a rollup increment: the whole deployment and, for a labelled server, its media node.
func scopes(mediaNode string) []string {
	if mediaNode == "" {
		return []string{globalScope}
	}
	return []string{globalScope, "node:" + mediaNode}
}

// counters adds the same counters to the hourly and daily documents of the node's scopes.
func (p *projector) counters(counters map[string]any) {
	p.result.Increments = append(p.result.Increments,
		marked(periodIncrements(p.opts.MediaNode, p.ts, counters), !p.opts.WithoutMediaStats)...)
}

// fiveMinute adds counters to the 5-minute documents of the node's scopes.
func (p *projector) fiveMinute(counters map[string]any) {
	p.result.Increments = append(p.result.Increments,
		marked(fiveMinuteIncrements(p.opts.MediaNode, p.ts, counters), !p.opts.WithoutMediaStats)...)
}

// globalCounters adds counters to the hourly and daily global documents only: egress and ingress run on a media node
// that their events do not name.
func (p *projector) globalCounters(counters map[string]any) {
	p.result.Increments = append(p.result.Increments,
		marked(periodIncrements("", p.ts, counters), !p.opts.WithoutMediaStats)...)
}

// globalFiveMinute adds counters to the 5-minute global document only.
func (p *projector) globalFiveMinute(counters map[string]any) {
	p.result.Increments = append(p.result.Increments,
		marked(fiveMinuteIncrements("", p.ts, counters), !p.opts.WithoutMediaStats)...)
}

func fiveMinuteIncrements(mediaNode string, at time.Time, counters map[string]any) []Increment {
	var increments []Increment
	for _, scope := range scopes(mediaNode) {
		increments = append(increments, Increment{
			Collection: schema.CollectionAnalytics5Min, Scope: scope, Bucket: at.Truncate(fiveMinutes), Counters: counters,
		})
	}
	return increments
}

func periodIncrements(mediaNode string, at time.Time, counters map[string]any) []Increment {
	var increments []Increment
	for _, scope := range scopes(mediaNode) {
		increments = append(increments,
			Increment{Collection: schema.CollectionAnalyticsHourly, Scope: scope, Bucket: at.Truncate(time.Hour), Counters: counters},
			Increment{Collection: schema.CollectionAnalyticsDaily, Scope: scope, Bucket: at.Truncate(24 * time.Hour), Counters: counters},
		)
	}
	return increments
}

// Peaks are the entities active at once in a scope: rooms, STANDARD participant sessions, egresses in progress and
// ingress sessions buffering or publishing.
type Peaks struct {
	Rooms        int32
	Participants int32
	Egress       int32
	Ingress      int32
}

// PeakIncrements returns the increments that keep, in the 5-minute, hourly and daily documents of scope containing at,
// the most entities active at once. Egress and ingress go to the global scope only, as their counters, and ingress to
// the hourly and daily documents only.
func PeakIncrements(scope string, at time.Time, peaks Peaks) []Increment {
	fiveMinute := map[string]any{"rooms.peakActive": peaks.Rooms, "participants.peakActive": peaks.Participants}
	if scope == globalScope {
		fiveMinute["egress.peakActive"] = peaks.Egress
	}
	period := maps.Clone(fiveMinute)
	if scope == globalScope {
		period["ingress.peakActive"] = peaks.Ingress
	}
	return []Increment{
		{Collection: schema.CollectionAnalytics5Min, Scope: scope, Bucket: at.Truncate(fiveMinutes), Maxima: fiveMinute},
		{Collection: schema.CollectionAnalyticsHourly, Scope: scope, Bucket: at.Truncate(time.Hour), Maxima: period},
		{Collection: schema.CollectionAnalyticsDaily, Scope: scope, Bucket: at.Truncate(24 * time.Hour), Maxima: period},
	}
}

// ClosedRoom is what the document of a closed room reads once the batch or reconciler run that closed it is applied:
// its STANDARD participant sessions and, when this writer version classified it, its topology, its most STANDARD
// sessions active at once and whether it was recorded.
type ClosedRoom struct {
	Sessions   int32
	PeakActive int32
	Topology   string
	Recorded   bool
}

var topologies = []string{TopologyEmpty, TopologySingleParticipant, TopologyNoPublishers, TopologyOneToOne,
	TopologyOneToMany, TopologyManyToMany}

// RoomEndedIncrements returns the increments of a room of mediaNode (empty without a label) closed at endedAt, once
// the batch knows its document: participantsSum and, for a room with a topology, its class, its recording and its
// size range. mediaStats is the room's mark.
func RoomEndedIncrements(mediaNode string, mediaStats bool, endedAt time.Time, room ClosedRoom) []Increment {
	counters := map[string]any{"rooms.participantsSum": room.Sessions}
	if containsString(topologies, room.Topology) {
		counters["rooms.byTopology."+room.Topology] = one
		if room.Recorded {
			counters["rooms.recorded"] = one
		}
		if sizedTopology(room.Topology) {
			counters["rooms.sizeByTopology."+room.Topology+"."+roomSizeRange(room.PeakActive)] = one
		}
	}
	return marked(periodIncrements(mediaNode, endedAt, counters), mediaStats)
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
		p.globalCounters(map[string]any{"egress.started": one, "egress.byType." + egressType(p.event.GetEgress()): one})
		p.globalFiveMinute(map[string]any{"egress.started": one})
	case livekit.AnalyticsEventType_EGRESS_ENDED:
		info := p.event.GetEgress()
		status := strings.ToLower(enumName(info.GetStatus().String(), "EGRESS_"))
		counters := map[string]any{"egress.endedByStatus." + status: one}
		if info.GetStartedAt() != 0 {
			ended := orTime(schema.UnixNanos(info.GetEndedAt()), p.ts)
			counters["egress.durationMs"] = schema.DurationMs(schema.UnixNanos(info.GetStartedAt()), ended)
		}
		p.globalCounters(counters)
		p.globalFiveMinute(map[string]any{"egress.ended": one})
	case livekit.AnalyticsEventType_INGRESS_STARTED:
		p.globalCounters(map[string]any{
			"ingress.sessionsStarted": one,
			"ingress.byInputType." + ingressInputType(p.event.GetIngress().GetInputType()): one,
		})
	case livekit.AnalyticsEventType_INGRESS_ENDED:
		state := p.event.GetIngress().GetState()
		counters := map[string]any{"ingress.sessionsEnded": one}
		if state.GetStartedAt() != 0 {
			ended := orTime(schema.UnixNanos(state.GetEndedAt()), p.ts)
			counters["ingress.durationMs"] = schema.DurationMs(schema.UnixNanos(state.GetStartedAt()), ended)
		}
		p.globalCounters(counters)
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
