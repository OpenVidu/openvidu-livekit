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
	"time"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

const (
	closureRetries    = 3
	closureRetryPause = 500 * time.Millisecond
)

// closer closes the confirmed candidates of a run one entity at a time, each only while it is still open, and
// collects the writes that follow the closures that happened so that they are applied together.
type closer struct {
	r         *reconciler
	report    *runReport
	remaining int
	deadline  time.Time
	stopped   bool

	cascades   []projection.Update
	related    []projection.Update
	raws       []schema.EventDocument
	increments []projection.Increment
	endedRooms []endedRoom
}

type endedRoom struct {
	sid    string
	nodeID string
	at     time.Time
}

// close closes the confirmed candidates within the closure limit and the time budget, and reports whether it stopped
// before trying all of them.
func (r *reconciler) close(ctx context.Context, confirmed confirmation, deadline time.Time, report *runReport) (bool, error) {
	c := &closer{r: r, report: report, remaining: r.settings.maxClosures, deadline: deadline}
	err := c.closeAll(ctx, confirmed)
	return c.stopped, errors.Join(err, c.apply(ctx))
}

func (c *closer) allowed() bool {
	if c.remaining <= 0 || !c.r.now().Before(c.deadline) {
		c.stopped = true
	}
	return !c.stopped
}

func (c *closer) add(closure projection.Closure) {
	c.remaining--
	c.cascades = append(c.cascades, closure.Cascades...)
	c.related = append(c.related, closure.Related...)
	c.raws = append(c.raws, closure.Raw)
	c.increments = append(c.increments, closure.Increments...)
}

func (c *closer) closeAll(ctx context.Context, confirmed confirmation) error {
	if err := c.closeParticipants(ctx, confirmed.participants); err != nil {
		return err
	}
	for _, room := range confirmed.rooms {
		if !c.allowed() {
			return nil
		}
		if err := c.closeRoom(ctx, room); err != nil {
			return err
		}
	}
	for _, candidate := range confirmed.egresses {
		if !c.allowed() {
			return nil
		}
		if err := c.closeEgress(ctx, candidate); err != nil {
			return err
		}
	}
	for _, candidate := range confirmed.ingressSessions {
		if !c.allowed() {
			return nil
		}
		if err := c.closeIngressSession(ctx, candidate); err != nil {
			return err
		}
	}
	return nil
}

func (c *closer) closeParticipants(ctx context.Context, participants []confirmedParticipant) error {
	if len(participants) == 0 {
		return nil
	}
	sids := make([]string, len(participants))
	for i, participant := range participants {
		sids[i] = participant.SID
	}
	_, published, err := c.r.store.Tracks(ctx, nil, sids)
	if err != nil {
		return err
	}
	for _, participant := range participants {
		if !c.allowed() {
			return nil
		}
		state := projection.ParticipantState{
			SID: participant.SID, RoomSID: participant.RoomSID, RoomName: participant.RoomName,
			Identity: participant.Identity, NodeID: participant.NodeID, Status: participant.Status,
			JoinedAt: participant.JoinedAt, TrackSIDs: published[participant.SID],
		}
		closure := projection.CloseParticipant(state, participant.at, c.vetoFrom(), participant.reason, c.r.opts)
		closed, err := c.r.store.CloseEntity(ctx, closure.Entity)
		if err != nil {
			return err
		}
		if !closed {
			c.skipped(entityParticipant, participant.SID)
			continue
		}
		c.add(closure)
		c.report.closed.Participant++
		c.r.logClosure(entityParticipant, participant.SID, participant.RoomSID, participant.NodeID, participant.reason,
			participant.LastStatsAt, participant.LastEventAt, "leftAt", participant.at)
	}
	return nil
}

// closeRoom closes a room, then its open participant sessions with the room's end time.
func (c *closer) closeRoom(ctx context.Context, room confirmedRoom) error {
	at := room.at
	if at.IsZero() {
		at = room.LastEventAt
		if room.LastStatsAt != nil && room.LastStatsAt.After(at) {
			at = *room.LastStatsAt
		}
	}
	state := projection.RoomState{SID: room.SID, Name: room.Name, NodeID: room.NodeID, CreatedAt: room.CreatedAt,
		LastEventAt: room.LastEventAt}
	closure := projection.CloseRoom(state, at, c.vetoFrom(), room.reason, c.r.opts)
	closed, err := c.r.store.CloseEntity(ctx, closure.Entity)
	if err != nil {
		return err
	}
	if !closed {
		c.skipped(entityRoom, room.SID)
		return nil
	}
	c.add(closure)
	c.endedRooms = append(c.endedRooms, endedRoom{sid: room.SID, nodeID: room.NodeID, at: at})
	c.report.closed.Room++
	c.r.logClosure(entityRoom, room.SID, room.SID, room.NodeID, room.reason, room.LastStatsAt, &room.LastEventAt,
		"endedAt", at)

	open, err := c.r.store.OpenParticipants(ctx, []string{room.SID})
	if err != nil {
		return err
	}
	cascaded := make([]confirmedParticipant, len(open))
	for i, participant := range open {
		if participant.RoomName == "" {
			participant.RoomName = room.Name
		}
		cascaded[i] = confirmedParticipant{OpenParticipant: participant, reason: reasonCascade, at: at}
	}
	return c.closeParticipants(ctx, cascaded)
}

func (c *closer) closeEgress(ctx context.Context, candidate egressCandidate) error {
	egress, room := candidate.egress, candidate.room
	at, reason := candidate.at, firstNonEmpty(candidate.reason, reasonRoomClosedNodeDead)
	if at.IsZero() {
		at = egress.UpdatedAt
		if room.EndedAt != nil && room.EndedAt.After(at) {
			at = *room.EndedAt
		}
	}
	state := projection.EgressState{ID: egress.ID, RoomSID: egress.RoomSID, RoomName: firstNonEmpty(egress.RoomName, room.Name),
		NodeID: room.NodeID, Status: egress.Status, StartedAt: egress.StartedAt}
	closure := projection.CloseEgress(state, at, reason, c.r.opts)
	closed, err := c.r.store.CloseEntity(ctx, closure.Entity)
	if err != nil {
		return err
	}
	if !closed {
		c.skipped(entityEgress, egress.ID)
		return nil
	}
	c.add(closure)
	c.report.closed.Egress++
	c.r.logClosure(entityEgress, egress.ID, egress.RoomSID, room.NodeID, reason, nil, &egress.UpdatedAt, "endedAt", at)
	return nil
}

func (c *closer) closeIngressSession(ctx context.Context, candidate ingressSessionCandidate) error {
	session, room := candidate.session, candidate.room
	at, reason := candidate.at, firstNonEmpty(candidate.reason, reasonRoomClosedNodeDead)
	if at.IsZero() {
		at = session.StartedAt
		if room.EndedAt != nil && room.EndedAt.After(at) {
			at = *room.EndedAt
		}
	}
	state := projection.IngressSessionState{ID: session.ID, IngressID: session.IngressID, RoomSID: session.RoomSID,
		RoomName: firstNonEmpty(session.RoomName, room.Name), NodeID: room.NodeID, Status: session.Status,
		StartedAt: session.StartedAt}
	closure := projection.CloseIngressSession(state, at, reason, c.r.opts)
	closed, err := c.r.store.CloseEntity(ctx, closure.Entity)
	if err != nil {
		return err
	}
	if !closed {
		c.skipped(entityIngressSession, session.ID)
		return nil
	}
	c.add(closure)
	c.report.closed.IngressSession++
	c.r.logClosure(entityIngressSession, session.ID, session.RoomSID, room.NodeID, reason, nil, nil, "endedAt", at)
	return nil
}

// vetoFrom is the instant after which a stats flush proves a room or a session alive.
func (c *closer) vetoFrom() time.Time {
	return c.r.now().Add(-statsInterval)
}

// skipped reports a closure that matched nothing: the entity received stats, changed status or was closed meanwhile.
func (c *closer) skipped(entity string, id string) {
	c.r.log.debugw("analytics reconciler: closure skipped, the entity changed since it was read", "entity", entity,
		"id", id)
}

// apply writes what follows the closures: participantsSum of the closed rooms, cascades, counters of related
// documents, synthetic events and rollups. Every step is attempted; transient errors are retried.
func (c *closer) apply(ctx context.Context) error {
	var errs []error
	if len(c.endedRooms) > 0 {
		sids := make([]string, len(c.endedRooms))
		for i, room := range c.endedRooms {
			sids[i] = room.sid
		}
		var sessions map[string]int32
		err := c.r.retry(ctx, func() (err error) {
			sessions, err = c.r.store.ParticipantSessions(ctx, sids)
			return err
		})
		errs = append(errs, err)
		for _, room := range c.endedRooms {
			c.increments = append(c.increments, projection.RoomEndedIncrements(room.nodeID, room.at, sessions[room.sid])...)
		}
	}
	if len(c.cascades) > 0 {
		errs = append(errs, c.r.retry(ctx, func() error {
			modified, err := c.r.store.ApplyCascadesCounted(ctx, c.cascades)
			c.report.closed.Track += int32(modified[schema.CollectionTracks])
			c.report.closed.Subscription += int32(modified[schema.CollectionTrackSubscriptions])
			return err
		}))
	}
	if len(c.related) > 0 {
		merged := projection.MergeUpdates(c.related, c.r.opts)
		errs = append(errs, c.r.retry(ctx, func() error { return c.r.store.ApplyUpdates(ctx, merged) }))
	}
	if len(c.raws) > 0 {
		errs = append(errs, c.r.retry(ctx, func() error {
			_, err := c.r.store.InsertEvents(ctx, c.raws)
			return err
		}))
	}
	if len(c.increments) > 0 {
		merged := projection.MergeIncrements(c.increments)
		errs = append(errs, c.r.retry(ctx, func() error { return c.r.store.ApplyIncrements(ctx, merged) }))
	}
	return errors.Join(errs...)
}

// retry runs step until it succeeds, fails for good or closureRetries attempts were made.
func (r *reconciler) retry(ctx context.Context, step func() error) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = step(); err == nil || !isTransient(err) || attempt == closureRetries || !sleep(ctx, closureRetryPause) {
			return err
		}
	}
}

func (r *reconciler) logClosure(entity string, id string, roomSid string, nodeID string, reason string,
	lastStatsAt *time.Time, lastEventAt *time.Time, endKey string, at time.Time) {
	fields := []any{"entity", entity, "id", id, "roomSid", roomSid, "nodeId", nodeID, "reason", reason}
	if lastStatsAt != nil {
		fields = append(fields, "lastStatsAt", *lastStatsAt)
	}
	if lastEventAt != nil {
		fields = append(fields, "lastEventAt", *lastEventAt)
	}
	r.log.infow("analytics reconciler: closed", append(fields, endKey, at)...)
}
