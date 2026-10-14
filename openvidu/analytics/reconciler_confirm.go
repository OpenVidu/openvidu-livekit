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
	"errors"
	"fmt"
	"time"

	"github.com/livekit/livekit-server/openvidu/analytics/store"
)

// keptSessionsLogged bounds the sessions a warning about kept candidates names.
const keptSessionsLogged = 10

var errEmptyListing = errors.New("LiveKit lists no room while rooms of alive nodes receive stats")

type confirmedParticipant struct {
	store.OpenParticipant
	reason string
	at     time.Time
}

type confirmedRoom struct {
	store.OpenRoom
	reason string
}

// confirmation holds the candidates of a run that are confirmed for closure.
type confirmation struct {
	participants    []confirmedParticipant
	rooms           []confirmedRoom
	egresses        []egressCandidate
	ingressSessions []ingressSessionCandidate
}

// confirm keeps the candidates to close. A candidate of a dead node is confirmed without LiveKit's live state: a dead
// process cannot have removed it from there. A candidate of an alive node is confirmed when it is absent from a trusted
// live listing in two consecutive runs. Egress and ingress session candidates are confirmed by detection.
func (r *reconciler) confirm(ctx context.Context, now time.Time, found candidateSet, report *runReport) (confirmation, error) {
	confirmed := confirmation{egresses: found.egresses, ingressSessions: found.ingressSessions}
	var aliveParticipants []store.OpenParticipant
	var aliveRooms []store.OpenRoom
	for _, participant := range found.participants {
		if found.dead[participant.NodeID] {
			confirmed.participants = append(confirmed.participants,
				confirmedParticipant{OpenParticipant: participant, reason: reasonNodeDead, at: endOfParticipant(participant)})
		} else {
			aliveParticipants = append(aliveParticipants, participant)
		}
	}
	for _, room := range found.rooms {
		if found.dead[room.NodeID] {
			confirmed.rooms = append(confirmed.rooms, confirmedRoom{OpenRoom: room, reason: reasonNodeDead})
		} else {
			aliveRooms = append(aliveRooms, room)
		}
	}

	if len(aliveParticipants)+len(aliveRooms) > 0 {
		listing, err := r.readListing(ctx, now)
		if err != nil {
			return confirmed, err
		}
		if listing.trusted {
			participants, rooms, err := r.confirmAbsent(ctx, listing, aliveParticipants, aliveRooms)
			if err != nil {
				return confirmed, err
			}
			if listing.trusted {
				confirmed.participants = append(confirmed.participants, participants...)
				confirmed.rooms = append(confirmed.rooms, rooms...)
			}
		}
		if !listing.trusted {
			clear(r.absent)
			report.result = listing.result
			r.log.warnw("analytics reconciler: LiveKit's live state is not trusted, only candidates of dead nodes are closed",
				listing.err, "result", listing.result)
		}
	}

	for id, run := range r.absent {
		if run < r.run-1 {
			delete(r.absent, id)
		}
	}
	return confirmed, nil
}

// confirmAbsent checks the candidates of alive nodes against the live listing and returns those absent in this run and
// in the previous one. A listing that fails meanwhile becomes untrusted.
func (r *reconciler) confirmAbsent(ctx context.Context, listing *liveListing, participants []store.OpenParticipant,
	rooms []store.OpenRoom) ([]confirmedParticipant, []confirmedRoom, error) {
	if err := r.fillRoomNames(ctx, participants); err != nil {
		return nil, nil, err
	}
	var confirmedParticipants []confirmedParticipant
	var kept []store.OpenParticipant
	for _, participant := range participants {
		if participant.RoomName == "" {
			r.log.debugw("analytics reconciler: candidate kept, its room name is unknown", "participantSid", participant.SID)
			continue
		}
		present, err := listing.hasParticipant(participant.RoomName, participant.SID)
		if err != nil {
			listing.untrust(resultLiveStateError, err)
			return nil, nil, nil
		}
		if present {
			delete(r.absent, participant.SID)
			kept = append(kept, participant)
			continue
		}
		if r.absentTwice(participant.SID) {
			confirmedParticipants = append(confirmedParticipants, confirmedParticipant{OpenParticipant: participant,
				reason: reasonAbsentLive, at: endOfParticipant(participant)})
		}
	}
	var confirmedRooms []confirmedRoom
	for _, room := range rooms {
		if listing.hasRoom(room.Name, room.SID) {
			delete(r.absent, room.SID)
			r.log.debugw("analytics reconciler: candidate kept, LiveKit still lists it", "roomSid", room.SID,
				"lastEventAt", room.LastEventAt)
			continue
		}
		if r.absentTwice(room.SID) {
			confirmedRooms = append(confirmedRooms, confirmedRoom{OpenRoom: room, reason: reasonAbsentLive})
		}
	}
	if len(kept) > 0 {
		r.warnKept(kept)
	}
	return confirmedParticipants, confirmedRooms, nil
}

// absentTwice records that a candidate is absent from the listing and reports whether the previous regular run saw
// it absent too.
func (r *reconciler) absentTwice(id string) bool {
	last, ok := r.absent[id]
	if ok && last == r.run-1 {
		delete(r.absent, id)
		return true
	}
	if !ok || last < r.run {
		r.absent[id] = r.run
	}
	return false
}

// fillRoomNames completes the room names the participant documents lack from their rooms.
func (r *reconciler) fillRoomNames(ctx context.Context, participants []store.OpenParticipant) error {
	var missing []string
	for _, participant := range participants {
		if participant.RoomName == "" {
			missing = append(missing, participant.RoomSID)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	rooms, err := r.store.Rooms(ctx, missing)
	if err != nil {
		return err
	}
	for i := range participants {
		if participants[i].RoomName == "" {
			participants[i].RoomName = rooms[participants[i].RoomSID].Name
		}
	}
	return nil
}

func (r *reconciler) warnKept(kept []store.OpenParticipant) {
	var sessions []string
	for _, participant := range kept[:min(len(kept), keptSessionsLogged)] {
		lastStats := "never"
		if participant.LastStatsAt != nil {
			lastStats = participant.LastStatsAt.Format(time.RFC3339)
		}
		sessions = append(sessions, fmt.Sprintf("%s (lastStatsAt %s)", participant.SID, lastStats))
	}
	r.log.warnw("analytics reconciler: sessions without recent stats are still in LiveKit's live state", nil,
		"count", len(kept), "sessions", sessions)
}

// liveListing is LiveKit's live state as read in one run: every room, and the participants of the rooms asked for.
type liveListing struct {
	live         liveState
	rooms        map[string]string
	participants map[string]map[string]bool
	trusted      bool
	result       string
	err          error
}

func (l *liveListing) untrust(result string, err error) {
	l.trusted = false
	l.result = result
	l.err = err
}

func (l *liveListing) hasRoom(name string, sid string) bool {
	return name != "" && l.rooms[name] == sid
}

func (l *liveListing) hasParticipant(roomName string, sid string) (bool, error) {
	if _, ok := l.rooms[roomName]; !ok {
		return false, nil
	}
	sids, ok := l.participants[roomName]
	if !ok {
		infos, err := l.live.ListParticipants(roomName)
		if err != nil {
			return false, err
		}
		sids = map[string]bool{}
		for _, info := range infos {
			sids[info.GetSid()] = true
		}
		l.participants[roomName] = sids
	}
	return sids[sid], nil
}

// readListing reads the rooms LiveKit lists. The listing is untrusted when the read fails, or when it lists no room
// while rooms of alive nodes still receive stats, as right after a Redis restart.
func (r *reconciler) readListing(ctx context.Context, now time.Time) (*liveListing, error) {
	listing := &liveListing{live: r.live, rooms: map[string]string{}, participants: map[string]map[string]bool{},
		trusted: true}
	rooms, err := r.live.ListActiveRooms()
	if err != nil {
		listing.untrust(resultLiveStateError, err)
		return listing, nil
	}
	for _, room := range rooms {
		listing.rooms[room.GetName()] = room.GetSid()
	}
	if len(rooms) > 0 {
		return listing, nil
	}
	nodes, err := r.store.LiveRoomNodes(ctx, now.Add(-staleAfter), liveRoomSample)
	if err != nil {
		return nil, err
	}
	dead, err := r.deadNodes(ctx, now, nodes)
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		if !dead[node] {
			listing.untrust(resultLiveStateUntrusted, errEmptyListing)
			break
		}
	}
	return listing, nil
}

// endOfParticipant estimates when a session ended: its last stats flush or its last event, whichever is later.
func endOfParticipant(participant store.OpenParticipant) time.Time {
	end := participant.JoinedAt
	for _, t := range []*time.Time{participant.ActiveAt, participant.LastEventAt, participant.LastStatsAt} {
		if t != nil && t.After(end) {
			end = *t
		}
	}
	return end
}
