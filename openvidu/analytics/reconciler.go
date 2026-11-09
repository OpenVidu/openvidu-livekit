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
	"time"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
)

// The reconciler closes the rooms, participant sessions, egress and ingress sessions that stay open in the store
// because their close event never arrived. One node at a time runs it, every reconciler.interval, under a lock.

const (
	// statsInterval is how often the server flushes the stats of every connected participant
	// (telemetryStatsUpdateInterval in pkg/telemetry); stats are the heartbeat of a session.
	statsInterval = 30 * time.Second
	// staleAfter is how long a session or a room goes without stats before it is a candidate.
	staleAfter = 3 * statsInterval
	// continuationDelay spaces the follow-up runs of a run that stopped at its closure limit or time budget.
	continuationDelay = 5 * time.Second
	// openMediaLimit bounds the open egresses and ingress sessions read per run.
	openMediaLimit = 10_000
	// liveRoomSample bounds the rooms read to decide whether an empty live listing can be trusted.
	liveRoomSample = 50

	reconcilerLockKey = "analytics-reconciler-lock"
)

// Results of a run, the label of reconciler_runs_total.
const (
	resultOK                 = "ok"
	resultPartial            = "partial"
	resultLiveStateError     = "live-state-error"
	resultLiveStateUntrusted = "live-state-untrusted"
	resultError              = "error"
)

// Reasons of a closure, in its log line and its synthetic event.
const (
	reasonNodeDead           = "node-dead"
	reasonAbsentLive         = "absent-live"
	reasonCascade            = "cascade"
	reasonRoomClosedNodeDead = "room-closed-node-dead"
	// reasonLegacyFixer closes an entity at the close event the legacy active-entities fixer inserted.
	reasonLegacyFixer = "legacy-fixer"
)

// reconcilerStore is what the reconciler needs from the database; *store.Store implements it.
type reconcilerStore interface {
	StaleParticipants(ctx context.Context, before time.Time, limit int) ([]store.OpenParticipant, error)
	OpenParticipants(ctx context.Context, roomSids []string) ([]store.OpenParticipant, error)
	RoomsWithOpenSessions(ctx context.Context, roomSids []string, excludedKinds []string) (map[string]bool, error)
	StaleRooms(ctx context.Context, now time.Time, grace time.Duration, statsFrom time.Time, limit int) ([]store.OpenRoom, error)
	IdleRooms(ctx context.Context, before time.Time, limit int) ([]store.OpenRoom, error)
	Rooms(ctx context.Context, sids []string) (map[string]store.RoomRef, error)
	OpenEgresses(ctx context.Context, limit int) ([]store.OpenEgress, error)
	OpenIngressSessions(ctx context.Context, limit int) ([]store.OpenIngressSession, error)
	NodesLastSeen(ctx context.Context, ids []string) (map[string]time.Time, error)
	LiveRoomNodes(ctx context.Context, statsFrom time.Time, limit int) ([]string, error)
	Tracks(ctx context.Context, trackSids []string, publisherSids []string) (map[string]projection.TrackRef, map[string][]string, error)
	ParticipantSessions(ctx context.Context, roomSids []string) (map[string]int32, error)
	CloseEntity(ctx context.Context, update projection.Update) (bool, error)
	CountReconciledRooms(ctx context.Context, rooms map[string]int32) error
	ApplyCascadesCounted(ctx context.Context, cascades []projection.Update) (map[string]int64, error)
	ApplyUpdates(ctx context.Context, updates []projection.Update) error
	InsertEvents(ctx context.Context, events []schema.EventDocument) (map[string]bool, error)
	ApplyIncrements(ctx context.Context, increments []projection.Increment) error
	RecordReconcilerRun(ctx context.Context, run schema.SchemaMetaReconciler) error
}

// liveState is LiveKit's live view of rooms, participants and nodes; livekithelper implements it.
type liveState interface {
	ListActiveRooms() ([]*livekit.Room, error)
	ListParticipants(roomName string) ([]*livekit.ParticipantInfo, error)
	IsNodeAvailable(nodeID string) (bool, error)
}

// reconcilerSettings holds the configuration the thresholds derive from.
type reconcilerSettings struct {
	// batchInterval is the writer's batch interval: a node without a heartbeat for two of them may be dead.
	batchInterval time.Duration
	// interval is reconciler.interval: the period of the runs and the room grace (two of them).
	interval time.Duration
	// cleanupInterval is LiveKit's dead node cleanup period: the egress and ingress grace (two of them).
	cleanupInterval time.Duration
	maxClosures     int
}

func (s reconcilerSettings) lockTTL() time.Duration {
	return s.interval + lockMargin
}

type reconciler struct {
	store    reconcilerStore
	live     liveState
	opts     projection.Options
	settings reconcilerSettings
	nodeID   string
	metrics  *reconcilerMetrics
	log      jobLog
	now      func() time.Time

	// run counts the regular runs; follow-ups of a partial run keep its number.
	run int
	// absent maps the candidates absent from a trusted live listing to the last regular run that saw them absent.
	absent map[string]int

	continuationDelay time.Duration
	lockErrorPause    time.Duration
	// holdLegacy reports a legacy migration that has not completed, during which candidates of nodes that never
	// wrote a heartbeat are not closed: they are entities the replay has not closed yet.
	holdLegacy func(ctx context.Context) (bool, error)
}

func newReconciler(s reconcilerStore, live liveState, opts projection.Options, settings reconcilerSettings, nodeID string,
	m *reconcilerMetrics) *reconciler {
	return &reconciler{store: s, live: live, opts: opts, settings: settings, nodeID: nodeID, metrics: m,
		log: defaultJobLog(), now: func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) },
		absent: map[string]int{}, continuationDelay: continuationDelay, lockErrorPause: lockErrorPause}
}

// runReport is the outcome of one run.
type runReport struct {
	startedAt  time.Time
	duration   time.Duration
	candidates schema.ReconcilerCounts
	closed     schema.ReconcilerClosures
	partial    bool
	result     string
	err        error
}

// reconcile runs once: it detects, confirms and closes candidates within the closure limit and the time budget, and
// records the run.
func (r *reconciler) reconcile(ctx context.Context, continuation bool) runReport {
	started := r.now()
	report := runReport{startedAt: started, result: resultOK}
	if !continuation {
		r.run++
	}
	deadline := started.Add(r.settings.lockTTL() - lockMargin)
	err := r.reconcileEntities(ctx, started, deadline, &report)
	switch {
	case err != nil:
		report.err = err
		report.result = resultError
	case report.partial && report.result == resultOK:
		report.result = resultPartial
	}
	report.duration = r.now().Sub(started)
	r.record(ctx, report)
	return report
}

func (r *reconciler) reconcileEntities(ctx context.Context, started time.Time, deadline time.Time, report *runReport) error {
	found, err := r.candidates(ctx, started)
	if err != nil {
		return err
	}
	report.candidates = found.counts()
	confirmed, err := r.confirm(ctx, started, found, report)
	if err != nil {
		return err
	}
	stopped, err := r.close(ctx, confirmed, deadline, report)
	if err != nil {
		return err
	}
	closedAny := report.closed.Room+report.closed.Participant+report.closed.Egress+report.closed.IngressSession > 0
	report.partial = stopped || (found.truncated && closedAny)
	return nil
}

func (r *reconciler) record(ctx context.Context, report runReport) {
	r.metrics.observe(report)
	run := schema.SchemaMetaReconciler{
		LastRunAt:         report.startedAt,
		LastRunDurationMs: report.duration.Milliseconds(),
		NodeID:            r.nodeID,
		Candidates:        report.candidates,
		Closed:            report.closed,
		Partial:           report.partial,
	}
	if report.err != nil {
		run.LastError = report.err.Error()
	}
	if err := r.store.RecordReconcilerRun(ctx, run); err != nil {
		r.log.warnw("analytics reconciler: cannot record the run", err)
	}

	fields := []any{"result", report.result, "durationMs", run.LastRunDurationMs, "partial", report.partial,
		"candidates", report.candidates, "closed", report.closed}
	switch {
	case report.err != nil:
		r.log.warnw("analytics reconciler: run failed", report.err, fields...)
	case report.result != resultOK || report.closed != (schema.ReconcilerClosures{}):
		r.log.infow("analytics reconciler: run", fields...)
	default:
		r.log.debugw("analytics reconciler: run", fields...)
	}
}

// loop runs the reconciler until ctx is cancelled, on one node at a time: once right after obtaining the lock for the
// first time, then every interval while holding it, with follow-up runs after a partial one.
func (r *reconciler) loop(ctx context.Context, locks locker) {
	job := &leaderJob{name: "analytics reconciler", key: reconcilerLockKey, interval: r.settings.interval,
		ttl: r.settings.lockTTL(), lockErrorPause: r.lockErrorPause, log: r.log, cycle: r.cycle}
	job.loop(ctx, locks)
}

func (r *reconciler) cycle(ctx context.Context, held lease, ttl time.Duration) {
	report := r.reconcile(ctx, false)
	for report.partial && ctx.Err() == nil {
		if !sleep(ctx, r.continuationDelay) || held.Refresh(ctx, ttl) != nil {
			return
		}
		report = r.reconcile(ctx, true)
	}
}
