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
	"time"

	"github.com/livekit/protocol/livekit"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// Reconciler closures end the entities whose close event never arrived, with an estimated end time and the same
// derived fields, counters and rollups as a real close event. A closure changes its entity only while the entity is
// still open; its other writes apply only when it did, so repeating a closure changes nothing.

const (
	// ClosedByReconciler is the closedBy of the entities the reconciler closed.
	ClosedByReconciler = "reconciler"
	// Reconciled is the end and disconnect reason of a reconciler closure.
	Reconciled = "RECONCILED"
	// LostEgressError is the error of an egress closed by the reconciler.
	LostEgressError = "egress lost (reconciled)"
	// LostIngressError is the error of an ingress session closed by the reconciler.
	LostIngressError = "ingress lost (reconciled)"
)

// Statuses the reconciler may close.
var (
	OpenParticipantStatuses    = []string{participantJoining, participantActive}
	OpenEgressStatuses         = []string{"starting", "active", "ending"}
	OpenIngressSessionStatuses = []string{"buffering", "publishing"}
)

// Closure is the write model of one reconciler closure.
type Closure struct {
	// Entity closes the document while it matches Filter: the status read by the reconciler and, for rooms and
	// participants, no stats flush since the veto instant.
	Entity Update
	// Cascades close the documents that depend on the entity.
	Cascades []Update
	// Related updates the counters that other documents keep about the entity; MergeUpdates adds their derive stage.
	Related []Update
	// Raw is the synthetic event that records the closure.
	Raw        schema.EventDocument
	Increments []Increment
}

// SyntheticEventID returns the _id of the raw event that records the closure of an entity.
func SyntheticEventID(entityID string) string {
	return "AE_SYN_" + entityID
}

// ClosedEntity returns the id of the entity a close event ends: participant session, room, egress or ingress session.
// It is empty for any other event.
func ClosedEntity(event *livekit.AnalyticsEvent) string {
	switch event.GetType() {
	case livekit.AnalyticsEventType_PARTICIPANT_LEFT, livekit.AnalyticsEventType_PARTICIPANT_CONNECTION_ABORTED:
		return firstNonEmpty(event.GetParticipantId(), event.GetParticipant().GetSid())
	case livekit.AnalyticsEventType_ROOM_ENDED:
		return firstNonEmpty(event.GetRoomId(), event.GetRoom().GetSid())
	case livekit.AnalyticsEventType_EGRESS_ENDED:
		return firstNonEmpty(event.GetEgressId(), event.GetEgress().GetEgressId())
	case livekit.AnalyticsEventType_INGRESS_ENDED:
		return event.GetIngress().GetState().GetResourceId()
	}
	return ""
}

// ParticipantState is what closing a participant session needs from its document.
type ParticipantState struct {
	SID      string
	RoomSID  string
	RoomName string
	Identity string
	NodeID   string
	Status   string
	JoinedAt time.Time
	// TrackSIDs are the tracks the participant published, whose open subscriptions close with it.
	TrackSIDs []string
}

// RoomState is what closing a room needs from its document.
type RoomState struct {
	SID         string
	Name        string
	NodeID      string
	CreatedAt   time.Time
	LastEventAt time.Time
}

// EgressState is what closing an egress needs from its document and its room.
type EgressState struct {
	ID        string
	RoomSID   string
	RoomName  string
	NodeID    string
	Status    string
	StartedAt time.Time
}

// IngressSessionState is what closing an ingress session needs from its document and its room.
type IngressSessionState struct {
	ID        string
	IngressID string
	RoomSID   string
	RoomName  string
	NodeID    string
	Status    string
	StartedAt time.Time
}

// noStatsSince matches documents without a stats flush at or after t.
func noStatsSince(t time.Time) bson.E {
	return bson.E{Key: "lastStatsAt", Value: bson.D{{Key: "$not", Value: bson.D{{Key: "$gte", Value: t}}}}}
}

func reconcilerPayload(reason string) bson.E {
	return bson.E{Key: "reconciler", Value: bson.D{{Key: "reason", Value: reason}}}
}

// CloseParticipant closes an open participant session at `at`, unless a stats flush arrived at or after vetoFrom: an
// active session leaves and a joining one aborts. reason explains the closure in the synthetic event.
func CloseParticipant(state ParticipantState, at time.Time, vetoFrom time.Time, reason string, opts Options) Closure {
	status, eventType, counter := participantLeft, livekit.AnalyticsEventType_PARTICIPANT_LEFT, "participants.left"
	if state.Status == participantJoining {
		status, eventType, counter = participantAborted, livekit.AnalyticsEventType_PARTICIPANT_CONNECTION_ABORTED,
			"participants.aborted"
	}

	f := newFields()
	f.set("status", status)
	f.set("leftAt", at)
	f.set("disconnectReason", Reconciled)
	f.set("closedBy", ClosedByReconciler)
	entity := Update{
		Collection: schema.CollectionParticipants,
		ID:         state.SID,
		Filter:     bson.D{{Key: "status", Value: state.Status}, noStatsSince(vetoFrom)},
		Pipeline:   []bson.D{stage(f.doc()), deriveStage(schema.CollectionParticipants, opts)},
	}

	tracksRetention, _, _ := opts.Retention.Of(schema.RetentionTracks)
	room := newFields()
	room.set("internal.closedSids", union("internal.closedSids", state.SID))
	if status == participantAborted {
		room.set("internal.abortedSids", union("internal.abortedSids", state.SID))
	}

	participant := bson.D{{Key: "sid", Value: state.SID}}
	if state.Identity != "" {
		participant = append(participant, bson.E{Key: "identity", Value: state.Identity})
	}
	participant = append(participant, bson.E{Key: "disconnect_reason", Value: Reconciled})

	counters := map[string]any{counter: one, "participants.disconnectReasons." + Reconciled: one}
	if status == participantLeft && !state.JoinedAt.IsZero() {
		counters["participants.minutes"] = float64(schema.DurationMs(state.JoinedAt, at)) / 60000
	}

	return Closure{
		Entity:   entity,
		Cascades: mediaCascades(state.SID, state.TrackSIDs, at, ClosedByReconciler, tracksRetention.Milliseconds()),
		Related: []Update{{Collection: schema.CollectionRooms, ID: state.RoomSID,
			Pipeline: []bson.D{stage(room.doc())}}},
		Raw: schema.EventDocument{
			ID:             SyntheticEventID(state.SID),
			Type:           eventType.String(),
			TS:             at,
			RoomSID:        state.RoomSID,
			RoomName:       state.RoomName,
			ParticipantSID: state.SID,
			Identity:       state.Identity,
			NodeID:         state.NodeID,
			Synthetic:      true,
			Payload: bson.D{
				{Key: "room", Value: roomPayload(state.RoomSID, state.RoomName)},
				{Key: "participant", Value: participant},
				reconcilerPayload(reason),
			},
		},
		Increments: append(periodIncrements(state.NodeID, at, counters),
			fiveMinuteIncrements(state.NodeID, at, map[string]any{counter: one})...),
	}
}

// CloseRoom closes an active room at `at`, unless an event or a stats flush reached it since the reconciler read it.
// Its open participant sessions are closed separately, and its participantsSum comes from RoomEndedIncrements.
func CloseRoom(state RoomState, at time.Time, vetoFrom time.Time, reason string, opts Options) Closure {
	f := newFields()
	f.set("status", roomClosed)
	f.set("endedAt", at)
	f.set("endReason", Reconciled)
	f.set("closedBy", ClosedByReconciler)
	f.set("internal.closedSids", bson.D{{Key: "$setUnion", Value: bson.A{
		emptyArray("internal.closedSids"), emptyArray("internal.activeSids")}}})

	counters := map[string]any{"rooms.ended": one, "rooms.endedByReason." + Reconciled: one}
	if !state.CreatedAt.IsZero() {
		counters["rooms.durationMsSum"] = schema.DurationMs(state.CreatedAt, at)
	}

	return Closure{
		Entity: Update{
			Collection: schema.CollectionRooms,
			ID:         state.SID,
			Filter: bson.D{{Key: "status", Value: roomActive}, {Key: "lastEventAt", Value: state.LastEventAt},
				noStatsSince(vetoFrom)},
			Pipeline: []bson.D{stage(f.doc()), deriveStage(schema.CollectionRooms, opts)},
		},
		Raw: schema.EventDocument{
			ID:        SyntheticEventID(state.SID),
			Type:      livekit.AnalyticsEventType_ROOM_ENDED.String(),
			TS:        at,
			RoomSID:   state.SID,
			RoomName:  state.Name,
			NodeID:    state.NodeID,
			Synthetic: true,
			Payload: bson.D{
				{Key: "room", Value: roomPayload(state.SID, state.Name)},
				{Key: "room_end_reason", Value: Reconciled},
				reconcilerPayload(reason),
			},
		},
		Increments: append(periodIncrements(state.NodeID, at, counters),
			fiveMinuteIncrements(state.NodeID, at, map[string]any{"rooms.ended": one})...),
	}
}

// CloseEgress ends an egress lost with its node as failed at `at`.
func CloseEgress(state EgressState, at time.Time, reason string, opts Options) Closure {
	const failed = "failed"
	f := newFields()
	f.set("status", failed)
	f.set("endedAt", at)
	f.set("updatedAt", maxDate("updatedAt", at))
	f.set("error", LostEgressError)
	f.set("closedBy", ClosedByReconciler)
	f.set("statusHistory", withStatus(failed, at))

	room := newFields()
	room.set("internal.endedEgressIds", union("internal.endedEgressIds", state.ID))

	counters := map[string]any{"egress.endedByStatus." + failed: one}
	if !state.StartedAt.IsZero() {
		counters["egress.durationMs"] = schema.DurationMs(state.StartedAt, at)
	}

	egress := bson.D{{Key: "egress_id", Value: state.ID}}
	if state.RoomSID != "" {
		egress = append(egress, bson.E{Key: "room_id", Value: state.RoomSID})
	}
	if state.RoomName != "" {
		egress = append(egress, bson.E{Key: "room_name", Value: state.RoomName})
	}
	egress = append(egress, bson.E{Key: "status", Value: livekit.EgressStatus_EGRESS_FAILED.String()},
		bson.E{Key: "error", Value: LostEgressError})

	return Closure{
		Entity: Update{
			Collection: schema.CollectionEgresses,
			ID:         state.ID,
			Filter:     bson.D{{Key: "status", Value: state.Status}},
			Pipeline:   []bson.D{stage(f.doc()), deriveStage(schema.CollectionEgresses, opts)},
		},
		Related: []Update{{Collection: schema.CollectionRooms, ID: state.RoomSID,
			Pipeline: []bson.D{stage(room.doc())}}},
		Raw: schema.EventDocument{
			ID:        SyntheticEventID(state.ID),
			Type:      livekit.AnalyticsEventType_EGRESS_ENDED.String(),
			TS:        at,
			RoomSID:   state.RoomSID,
			RoomName:  state.RoomName,
			EgressID:  state.ID,
			NodeID:    state.NodeID,
			Synthetic: true,
			Payload:   bson.D{{Key: "egress", Value: egress}, reconcilerPayload(reason)},
		},
		Increments: append(periodIncrements(state.NodeID, at, counters),
			fiveMinuteIncrements(state.NodeID, at, map[string]any{"egress.ended": one})...),
	}
}

// CloseIngressSession ends an ingress session lost with its node as error at `at`, and records the ended session on
// its ingress, whose status becomes error unless a later state replaced it.
func CloseIngressSession(state IngressSessionState, at time.Time, reason string, opts Options) Closure {
	const failed = "error"
	f := newFields()
	f.set("status", failed)
	f.set("endedAt", at)
	f.set("error", LostIngressError)
	f.set("closedBy", ClosedByReconciler)

	duration := schema.DurationMs(state.StartedAt, at)
	newest := gte(at, ifNull("internal.stateAt", time.Unix(0, 0).UTC()))
	ingress := newFields()
	ingress.set("status", cond(eq(field("status"), ingressDeleted), ingressDeleted, cond(newest, failed, field("status"))))
	ingress.set("lastError", cond(newest, LostIngressError, field("lastError")))
	ingress.set("internal.stateAt", maxDate("internal.stateAt", at))
	ingress.set("internal.endedSessionDurations."+state.ID, duration)

	room := newFields()
	room.set("internal.endedIngressSessionIds", union("internal.endedIngressSessionIds", state.ID))

	counters := map[string]any{"ingress.sessionsEnded": one}
	if !state.StartedAt.IsZero() {
		counters["ingress.durationMs"] = duration
	}

	ingressPayload := bson.D{{Key: "ingress_id", Value: state.IngressID}}
	if state.RoomName != "" {
		ingressPayload = append(ingressPayload, bson.E{Key: "room_name", Value: state.RoomName})
	}
	ingressPayload = append(ingressPayload, bson.E{Key: "state", Value: bson.D{
		{Key: "status", Value: livekit.IngressState_ENDPOINT_ERROR.String()},
		{Key: "error", Value: LostIngressError},
		{Key: "room_id", Value: state.RoomSID},
		{Key: "resource_id", Value: state.ID},
	}})

	related := []Update{{Collection: schema.CollectionIngresses, ID: state.IngressID,
		Pipeline: []bson.D{stage(ingress.doc())}}}
	if state.RoomSID != "" {
		related = append(related, Update{Collection: schema.CollectionRooms, ID: state.RoomSID,
			Pipeline: []bson.D{stage(room.doc())}})
	}

	return Closure{
		Entity: Update{
			Collection: schema.CollectionIngressSessions,
			ID:         state.ID,
			Filter:     bson.D{{Key: "status", Value: state.Status}},
			Pipeline:   []bson.D{stage(f.doc()), deriveStage(schema.CollectionIngressSessions, opts)},
		},
		Related: related,
		Raw: schema.EventDocument{
			ID:               SyntheticEventID(state.ID),
			Type:             livekit.AnalyticsEventType_INGRESS_ENDED.String(),
			TS:               at,
			RoomSID:          state.RoomSID,
			RoomName:         state.RoomName,
			IngressID:        state.IngressID,
			IngressSessionID: state.ID,
			NodeID:           state.NodeID,
			Synthetic:        true,
			Payload:          bson.D{{Key: "ingress", Value: ingressPayload}, reconcilerPayload(reason)},
		},
		Increments: periodIncrements(state.NodeID, at, counters),
	}
}

func roomPayload(sid string, name string) bson.D {
	room := bson.D{{Key: "sid", Value: sid}}
	if name != "" {
		room = append(room, bson.E{Key: "name", Value: name})
	}
	return room
}
