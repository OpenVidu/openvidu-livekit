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
	"sync"
	"time"

	"github.com/bsm/redislock"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
)

// The reconciler closes the rooms, participant sessions, egress and ingress sessions that stay open in the store
// because their close event never arrived. One node at a time runs it, every fixer_interval, under a lock.

const (
	// statsInterval is how often the server flushes the stats of every connected participant
	// (telemetryStatsUpdateInterval in pkg/telemetry); stats are the heartbeat of a session.
	statsInterval = 30 * time.Second
	// staleAfter is how long a session or a room goes without stats before it is a candidate.
	staleAfter = 3 * statsInterval
	// lockMargin is added to fixer_interval for the lock TTL and kept free at the end of the run's time budget.
	lockMargin = 5 * time.Second
	// continuationDelay spaces the follow-up runs of a run that stopped at its closure limit or time budget.
	continuationDelay = 5 * time.Second
	// lockErrorPause spaces the attempts to obtain the lock while Redis fails.
	lockErrorPause = 5 * time.Second
	// openMediaLimit bounds the open egresses and ingress sessions read per run.
	openMediaLimit = 10_000
	// liveRoomSample bounds the rooms read to decide whether an empty live listing can be trusted.
	liveRoomSample = 50
	// uniqueIdentityDaysPerRun bounds the days whose unique identities one run computes.
	uniqueIdentityDaysPerRun = 7
	// dayCloseGrace leaves time for the last batches of a UTC day before its unique identities are computed.
	dayCloseGrace = 5 * time.Minute

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
)

// reconcilerStore is what the reconciler needs from the database; *store.Store implements it.
type reconcilerStore interface {
	StaleParticipants(ctx context.Context, before time.Time, limit int) ([]store.OpenParticipant, error)
	OpenParticipants(ctx context.Context, roomSids []string) ([]store.OpenParticipant, error)
	RoomsWithOpenSessions(ctx context.Context, roomSids []string, excludedKinds []string) (map[string]bool, error)
	StaleRooms(ctx context.Context, now time.Time, grace time.Duration, statsFrom time.Time, limit int) ([]store.OpenRoom, error)
	Rooms(ctx context.Context, sids []string) (map[string]store.RoomRef, error)
	OpenEgresses(ctx context.Context, limit int) ([]store.OpenEgress, error)
	OpenIngressSessions(ctx context.Context, limit int) ([]store.OpenIngressSession, error)
	NodesLastSeen(ctx context.Context, ids []string) (map[string]time.Time, error)
	LiveRoomNodes(ctx context.Context, statsFrom time.Time, limit int) ([]string, error)
	Tracks(ctx context.Context, trackSids []string, publisherSids []string) (map[string]projection.TrackRef, map[string][]string, error)
	ParticipantSessions(ctx context.Context, roomSids []string) (map[string]int32, error)
	CloseEntity(ctx context.Context, update projection.Update) (bool, error)
	ApplyCascadesCounted(ctx context.Context, cascades []projection.Update) (map[string]int64, error)
	ApplyUpdates(ctx context.Context, updates []projection.Update) error
	InsertEvents(ctx context.Context, events []schema.EventDocument) (map[string]bool, error)
	ApplyIncrements(ctx context.Context, increments []projection.Increment) error
	DaysWithoutUniqueIdentities(ctx context.Context, from time.Time, to time.Time, limit int) ([]time.Time, error)
	UniqueIdentities(ctx context.Context, from time.Time, to time.Time) (int32, map[string]int32, error)
	SetUniqueIdentities(ctx context.Context, day time.Time, global int32, perNode map[string]int32) error
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
	// interval is fixer_interval: the period of the runs and the room grace (two of them).
	interval time.Duration
	// cleanupInterval is LiveKit's dead node cleanup period: the egress and ingress grace (two of them).
	cleanupInterval   time.Duration
	maxClosures       int
	entitiesRetention time.Duration
}

func (s reconcilerSettings) lockTTL() time.Duration {
	return s.interval + lockMargin
}

// reconcilerLog writes the reconciler's log lines.
type reconcilerLog struct {
	debugw func(msg string, keysAndValues ...any)
	infow  func(msg string, keysAndValues ...any)
	warnw  func(msg string, err error, keysAndValues ...any)
}

func defaultReconcilerLog() reconcilerLog {
	return reconcilerLog{debugw: logger.Debugw, infow: logger.Infow, warnw: logger.Warnw}
}

type reconciler struct {
	store    reconcilerStore
	live     liveState
	opts     projection.Options
	settings reconcilerSettings
	nodeID   string
	metrics  *reconcilerMetrics
	log      reconcilerLog
	now      func() time.Time

	// run counts the regular runs; follow-ups of a partial run keep its number.
	run int
	// absent maps the candidates absent from a trusted live listing to the last regular run that saw them absent.
	absent map[string]int
	// releaseFailed silences repeated warnings while the lock cannot be released.
	releaseFailed bool

	continuationDelay time.Duration
	lockErrorPause    time.Duration
}

func newReconciler(s reconcilerStore, live liveState, opts projection.Options, settings reconcilerSettings, nodeID string,
	m *reconcilerMetrics) *reconciler {
	return &reconciler{store: s, live: live, opts: opts, settings: settings, nodeID: nodeID, metrics: m,
		log: defaultReconcilerLog(), now: func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) },
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

// reconcile runs once: it detects, confirms and closes candidates within the closure limit and the time budget, then
// computes the unique identities of closed days, and records the run.
func (r *reconciler) reconcile(ctx context.Context, continuation bool) runReport {
	started := r.now()
	report := runReport{startedAt: started, result: resultOK}
	if !continuation {
		r.run++
	}
	deadline := started.Add(r.settings.lockTTL() - lockMargin)
	err := r.reconcileEntities(ctx, started, deadline, &report)
	if err == nil && r.now().Before(deadline) {
		err = r.dailyUniqueIdentities(ctx, started)
	}
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

// dailyUniqueIdentities sets the unique identities of the closed UTC days inside the entities retention that lack them.
func (r *reconciler) dailyUniqueIdentities(ctx context.Context, now time.Time) error {
	day := 24 * time.Hour
	from := now.Add(-r.settings.entitiesRetention).Truncate(day).Add(day)
	to := now.Add(-dayCloseGrace).Truncate(day)
	days, err := r.store.DaysWithoutUniqueIdentities(ctx, from, to, uniqueIdentityDaysPerRun)
	if err != nil {
		return err
	}
	for _, start := range days {
		global, perNode, err := r.store.UniqueIdentities(ctx, start, start.Add(day))
		if err != nil {
			return err
		}
		if err := r.store.SetUniqueIdentities(ctx, start, global, perNode); err != nil {
			return err
		}
		r.log.debugw("analytics reconciler: unique identities of a closed day", "day", start, "global", global,
			"nodes", len(perNode))
	}
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

// locker elects the node that runs the reconciler.
type locker interface {
	// Obtain returns a held lease, or an error wrapping redislock.ErrNotObtained when another node holds it.
	Obtain(ctx context.Context, key string, ttl time.Duration) (lease, error)
}

type lease interface {
	Refresh(ctx context.Context, ttl time.Duration) error
	Release(ctx context.Context) error
}

// redisLocker obtains the lock in Redis, retrying until its TTL elapses.
type redisLocker struct {
	client  *redislock.Client
	backoff time.Duration
}

func (l redisLocker) Obtain(ctx context.Context, key string, ttl time.Duration) (lease, error) {
	lock, err := l.client.Obtain(ctx, key, ttl, &redislock.Options{RetryStrategy: redislock.LinearBackoff(l.backoff)})
	if err != nil {
		return nil, err
	}
	return redisLease{lock}, nil
}

type redisLease struct {
	lock *redislock.Lock
}

func (l redisLease) Refresh(ctx context.Context, ttl time.Duration) error {
	return l.lock.Refresh(ctx, ttl, nil)
}

func (l redisLease) Release(ctx context.Context) error {
	return l.lock.Release(ctx)
}

// localLocker elects the only node of a deployment without Redis.
type localLocker struct {
	mu sync.Mutex
}

func (l *localLocker) Obtain(context.Context, string, time.Duration) (lease, error) {
	if !l.mu.TryLock() {
		return nil, redislock.ErrNotObtained
	}
	return localLease{&l.mu}, nil
}

type localLease struct {
	mu *sync.Mutex
}

func (localLease) Refresh(context.Context, time.Duration) error {
	return nil
}

func (l localLease) Release(context.Context) error {
	l.mu.Unlock()
	return nil
}

// loop runs the reconciler until ctx is cancelled: once right after obtaining the lock for the first time, then every
// interval while holding it, with follow-up runs after a partial one.
func (r *reconciler) loop(ctx context.Context, locks locker) {
	ttl := r.settings.lockTTL()
	startup := true
	lockFailing := false
	for ctx.Err() == nil {
		held, err := locks.Obtain(ctx, reconcilerLockKey, ttl)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !errors.Is(err, redislock.ErrNotObtained) {
				if !lockFailing {
					r.log.warnw("analytics reconciler: cannot obtain the lock, retrying", err, "pause", r.lockErrorPause)
					lockFailing = true
				}
				sleep(ctx, r.lockErrorPause)
			}
			continue
		}
		lockFailing = false
		if !startup {
			if !sleep(ctx, r.settings.interval) || held.Refresh(ctx, ttl) != nil {
				r.release(held)
				continue
			}
		}
		startup = false
		r.cycle(ctx, held, ttl)
		r.release(held)
	}
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

// release frees the lock; after a Redis restart the lock is gone, which is harmless.
func (r *reconciler) release(held lease) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := held.Release(ctx); err != nil {
		if !r.releaseFailed {
			r.log.infow("analytics reconciler: lock not released, it expires with its TTL", "error", err.Error())
			r.releaseFailed = true
		}
		return
	}
	r.releaseFailed = false
}

// sleep waits for d and reports false when ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
