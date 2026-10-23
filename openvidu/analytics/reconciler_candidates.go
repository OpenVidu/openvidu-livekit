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
	"context"
	"slices"
	"time"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
)

// candidateSet holds the candidates of a run and the liveness of their nodes.
type candidateSet struct {
	participants []store.OpenParticipant
	// rooms holds the room candidates of alive nodes, deadRooms those of dead nodes, whose open sessions are checked
	// after the run's participant closures.
	rooms           []store.OpenRoom
	deadRooms       []store.OpenRoom
	egresses        []egressCandidate
	ingressSessions []ingressSessionCandidate
	// dead holds the nodes of the candidates that are dead.
	dead map[string]bool
	// truncated reports that a candidate query hit the closure limit, so more candidates may exist.
	truncated bool
}

// egressCandidate is an open egress whose room closed long ago on a dead node.
type egressCandidate struct {
	egress store.OpenEgress
	room   store.RoomRef
	// reason and at override the closure reason and the end time estimated from the room.
	reason string
	at     time.Time
}

// ingressSessionCandidate is an open ingress session whose room closed long ago on a dead node.
type ingressSessionCandidate struct {
	session store.OpenIngressSession
	room    store.RoomRef
	reason  string
	at      time.Time
}

func (c candidateSet) counts() schema.ReconcilerCounts {
	return schema.ReconcilerCounts{
		Room:           int32(len(c.rooms) + len(c.deadRooms)),
		Participant:    int32(len(c.participants)),
		Egress:         int32(len(c.egresses)),
		IngressSession: int32(len(c.ingressSessions)),
	}
}

// candidates detects, from the store alone:
//   - participant sessions without stats for staleAfter (or, without any stats, active or joined that long ago);
//   - active rooms without stats for staleAfter, without an open session of a kind that keeps a room open, whose
//     last event is older than their empty timeout plus two intervals;
//   - active rooms of dead nodes without stats or events for staleAfter, whatever their open sessions;
//   - open egresses and ingress sessions whose room closed more than two cleanup intervals ago on a dead node.
func (r *reconciler) candidates(ctx context.Context, now time.Time) (candidateSet, error) {
	hold := false
	if r.holdLegacy != nil {
		var err error
		if hold, err = r.holdLegacy(ctx); err != nil {
			return candidateSet{}, err
		}
	}
	limit := r.settings.maxClosures
	staleBefore := now.Add(-staleAfter)
	var found candidateSet

	participants, err := r.store.StaleParticipants(ctx, staleBefore, limit)
	if err != nil {
		return found, err
	}
	rooms, err := r.store.StaleRooms(ctx, now, 2*r.settings.interval, staleBefore, limit)
	if err != nil {
		return found, err
	}
	idle, err := r.store.IdleRooms(ctx, staleBefore, limit)
	if err != nil {
		return found, err
	}
	found.participants = participants
	found.truncated = len(participants) >= limit || len(rooms) >= limit || len(idle) >= limit

	egresses, sessions, err := r.mediaOfClosedRooms(ctx, now)
	if err != nil {
		return found, err
	}

	var nodes []string
	for _, participant := range found.participants {
		nodes = append(nodes, participant.NodeID)
	}
	for _, room := range slices.Concat(rooms, idle) {
		nodes = append(nodes, room.NodeID)
	}
	for _, candidate := range egresses {
		nodes = append(nodes, candidate.room.NodeID)
	}
	for _, candidate := range sessions {
		nodes = append(nodes, candidate.room.NodeID)
	}
	var seen map[string]bool
	found.dead, seen, err = r.deadNodes(ctx, now, nodes)
	if err != nil {
		return found, err
	}
	if found.rooms, found.deadRooms, err = r.splitRooms(ctx, rooms, idle, found.dead); err != nil {
		return found, err
	}
	if hold {
		// The legacy migration replays entities of nodes that never wrote a heartbeat, closed later in the replay.
		legacy := func(node string) bool { return !seen[node] }
		held := len(found.participants) + len(found.rooms) + len(found.deadRooms) + len(egresses) + len(sessions)
		legacyRoom := func(room store.OpenRoom) bool { return legacy(room.NodeID) }
		found.participants = slices.DeleteFunc(found.participants, func(p store.OpenParticipant) bool { return legacy(p.NodeID) })
		found.rooms = slices.DeleteFunc(found.rooms, legacyRoom)
		found.deadRooms = slices.DeleteFunc(found.deadRooms, legacyRoom)
		egresses = slices.DeleteFunc(egresses, func(c egressCandidate) bool { return legacy(c.room.NodeID) })
		sessions = slices.DeleteFunc(sessions, func(c ingressSessionCandidate) bool { return legacy(c.room.NodeID) })
		if held -= len(found.participants) + len(found.rooms) + len(found.deadRooms) + len(egresses) + len(sessions); held > 0 {
			r.log.debugw("analytics reconciler: candidates of legacy nodes held until the migration completes", "count", held)
		}
	}
	for _, candidate := range egresses {
		if found.dead[candidate.room.NodeID] {
			found.egresses = append(found.egresses, candidate)
		}
	}
	for _, candidate := range sessions {
		if found.dead[candidate.room.NodeID] {
			found.ingressSessions = append(found.ingressSessions, candidate)
		}
	}
	return found, nil
}

// splitRooms returns the timeout candidates of alive nodes without an open session of a kind that keeps a room open,
// and the rooms of dead nodes among the timeout candidates and the idle rooms; idle rooms of alive nodes are dropped.
func (r *reconciler) splitRooms(ctx context.Context, timedOut []store.OpenRoom, idle []store.OpenRoom,
	dead map[string]bool) ([]store.OpenRoom, []store.OpenRoom, error) {
	var alive, deadRooms []store.OpenRoom
	listed := map[string]bool{}
	for _, room := range timedOut {
		listed[room.SID] = true
		if dead[room.NodeID] {
			deadRooms = append(deadRooms, room)
		} else {
			alive = append(alive, room)
		}
	}
	for _, room := range idle {
		if dead[room.NodeID] && !listed[room.SID] {
			listed[room.SID] = true
			deadRooms = append(deadRooms, room)
		}
	}
	if len(alive) == 0 {
		return nil, deadRooms, nil
	}
	sids := make([]string, len(alive))
	for i, room := range alive {
		sids[i] = room.SID
	}
	keptOpen, err := r.store.RoomsWithOpenSessions(ctx, sids, projection.DependentKinds)
	if err != nil {
		return nil, nil, err
	}
	alive = slices.DeleteFunc(alive, func(room store.OpenRoom) bool { return keptOpen[room.SID] })
	return alive, deadRooms, nil
}

// mediaOfClosedRooms returns the open egresses and ingress sessions whose room closed more than two cleanup intervals
// ago: LiveKit ends the egress and ingress of a dead node itself, with real events, after its dead node cleanup.
func (r *reconciler) mediaOfClosedRooms(ctx context.Context, now time.Time) ([]egressCandidate, []ingressSessionCandidate, error) {
	egresses, err := r.store.OpenEgresses(ctx, openMediaLimit)
	if err != nil {
		return nil, nil, err
	}
	sessions, err := r.store.OpenIngressSessions(ctx, openMediaLimit)
	if err != nil {
		return nil, nil, err
	}
	if len(egresses)+len(sessions) == 0 {
		return nil, nil, nil
	}
	var roomSids []string
	for _, egress := range egresses {
		roomSids = append(roomSids, egress.RoomSID)
	}
	for _, session := range sessions {
		roomSids = append(roomSids, session.RoomSID)
	}
	rooms, err := r.store.Rooms(ctx, roomSids)
	if err != nil {
		return nil, nil, err
	}
	closedBefore := now.Add(-2 * r.settings.cleanupInterval)
	closedLongAgo := func(sid string) (store.RoomRef, bool) {
		room, ok := rooms[sid]
		return room, ok && room.Status == "closed" && room.EndedAt != nil && room.EndedAt.Before(closedBefore)
	}

	var egressCandidates []egressCandidate
	for _, egress := range egresses {
		if room, ok := closedLongAgo(egress.RoomSID); ok {
			egressCandidates = append(egressCandidates, egressCandidate{egress: egress, room: room})
		}
	}
	var sessionCandidates []ingressSessionCandidate
	for _, session := range sessions {
		if room, ok := closedLongAgo(session.RoomSID); ok {
			sessionCandidates = append(sessionCandidates, ingressSessionCandidate{session: session, room: room})
		}
	}
	return egressCandidates, sessionCandidates, nil
}

// deadNodes returns the nodes among ids that are dead (no heartbeat for two batch intervals and not available in
// LiveKit's node registry) and those that ever wrote a heartbeat. A node whose registry entry cannot be read is not
// dead.
func (r *reconciler) deadNodes(ctx context.Context, now time.Time, ids []string) (map[string]bool, map[string]bool, error) {
	dead := map[string]bool{}
	seen := map[string]bool{}
	var unique []string
	listed := map[string]bool{}
	for _, id := range ids {
		if id != "" && !listed[id] {
			listed[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return dead, seen, nil
	}
	lastSeen, err := r.store.NodesLastSeen(ctx, unique)
	if err != nil {
		return nil, nil, err
	}
	for id := range lastSeen {
		seen[id] = true
	}
	heartbeatFrom := now.Add(-2 * r.settings.batchInterval)
	for _, id := range unique {
		if at, ok := lastSeen[id]; ok && !at.Before(heartbeatFrom) {
			continue
		}
		available, err := r.live.IsNodeAvailable(id)
		if err != nil {
			r.log.debugw("analytics reconciler: node registry unreadable, node kept alive", "nodeId", id,
				"error", err.Error())
			continue
		}
		if !available {
			dead[id] = true
		}
	}
	return dead, seen, nil
}
