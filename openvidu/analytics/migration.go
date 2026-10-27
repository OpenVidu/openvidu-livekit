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
	"math"
	"time"

	"github.com/bsm/redislock"
	"github.com/livekit/protocol/livekit"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	legacy "github.com/livekit/livekit-server/openvidu/analytics/migration"
	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
)

// The legacy migration replays the `events` (renamed `legacy_events` at startup) and `stats` collections of the legacy
// writer through the live projections, on one node at a time, in the background and resumable after any restart.

// States of the migration in schema_meta.
const (
	migrationPending   = "pending"
	migrationRunning   = "running"
	migrationCompleted = "completed"
	migrationFailed    = "failed"
)

// Phases of the migration, in order.
const (
	phaseEvents    = "events"
	phaseStats     = "stats"
	phaseReconcile = "reconcile"
	phaseCleanup   = "cleanup"
)

const (
	migrationLockKey = "analytics-migration-lock"
	// migrationLockTTL bounds how long a crashed runner keeps the migration; the lock is refreshed after every batch.
	migrationLockTTL = 60 * time.Second
	// migrationLockRetry spaces the attempts of a node that does not hold the migration lock.
	migrationLockRetry = time.Minute
	// migrationProgressInterval spaces the progress log lines.
	migrationProgressInterval = 30 * time.Second
	// migrationRetries and migrationBackoff bound the retries of a failing batch: 1, 2, 4 and 8 s.
	migrationRetries = 5
	migrationBackoff = time.Second

	legacyStatsCollection          = "stats"
	legacyActiveEntitiesCollection = "active_entities"
	legacyLastAliveCollection      = "last_alive"
)

// errLockLost stops a runner that could not refresh its lock: another node may resume the migration.
var errLockLost = errors.New("migration lock lost")

// legacyCollections returns the collections of the legacy writer present in the database.
func legacyCollections(ctx context.Context, db *mongo.Database) ([]string, error) {
	return db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: bson.D{{Key: "$in", Value: bson.A{
		LegacyEventsCollection, legacyStatsCollection, legacyActiveEntitiesCollection, legacyLastAliveCollection}}}}})
}

// prepareMigration records a pending migration when legacy collections exist and no migration was recorded, and
// reports whether a migration remains to run.
func prepareMigration(ctx context.Context, db *mongo.Database, writerVersion string) (bool, error) {
	meta := db.Collection(schema.CollectionSchemaMeta)
	var existing schema.SchemaMetaMigration
	err := meta.FindOne(ctx, bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}}).Decode(&existing)
	if err == nil {
		return existing.State != migrationCompleted, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return false, err
	}
	collections, err := legacyCollections(ctx, db)
	if err != nil || len(collections) == 0 {
		return false, err
	}
	var total int64
	for _, name := range []string{LegacyEventsCollection, legacyStatsCollection} {
		n, err := db.Collection(name).EstimatedDocumentCount(ctx)
		if err != nil {
			return false, err
		}
		total += n
	}
	pending, err := bson.Marshal(schema.SchemaMetaMigration{
		State: migrationPending, Checkpoint: &schema.MigrationPoint{Phase: phaseEvents}, Total: total,
		WriterVersion: writerVersion,
	})
	if err != nil {
		return false, err
	}
	var fields bson.D
	if err := bson.Unmarshal(pending, &fields); err != nil {
		return false, err
	}
	fields = fields[1:]
	_, err = meta.UpdateOne(ctx, bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}},
		bson.D{{Key: "$setOnInsert", Value: fields}}, options.UpdateOne().SetUpsert(true))
	return true, err
}

// migrator replays the legacy collections. It owns a processor and a reconciler of its own, next to the live ones.
type migrator struct {
	db         *mongo.Database
	store      *store.Store
	processor  *processor
	reconciler *reconciler
	settings   openviduconfig.MigrationSettings
	nodeID     string
	locks      locker
	metrics    *migrationMetrics
	log        reconcilerLog
	now        func() time.Time
	backoff    time.Duration
	lockRetry  time.Duration

	state        schema.SchemaMetaMigration
	progressAt   time.Time
	progressFrom int64
}

func newMigrator(db *mongo.Database, settings openviduconfig.AnalyticsSettings, rec *reconciler, locks locker, nodeID string,
	m *migrationMetrics) *migrator {
	opts := projectionOptions(settings)
	mig := &migrator{db: db, store: store.New(db), reconciler: rec, settings: settings.Migration, nodeID: nodeID,
		locks: locks, metrics: m, log: defaultReconcilerLog(),
		now:     func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) },
		backoff: migrationBackoff, lockRetry: migrationLockRetry}
	mig.processor = &processor{store: store.New(db), opts: opts, cache: newRefCache(),
		onWriteErrors: func(err *store.WriteError) {
			mig.log.warnw("analytics migration: writes rejected and dropped", err.Err,
				"collection", err.Collection, "count", err.Count)
		}}
	return mig
}

// loop runs the migration on this node once it holds the migration lock, and returns when the migration has completed,
// has failed (the next start retries it) or is disabled.
func (m *migrator) loop(ctx context.Context) {
	waiting := false
	for ctx.Err() == nil {
		found, err := m.load(ctx)
		if err != nil {
			m.log.warnw("analytics migration: cannot read its state", err)
			if !sleep(ctx, m.lockRetry) {
				return
			}
			continue
		}
		if !found || m.state.State == migrationCompleted {
			m.metrics.setState(m.state.State)
			return
		}
		m.metrics.setState(m.state.State)
		if !m.settings.Enabled {
			m.log.warnw("analytics migration: legacy data left unmigrated because openvidu.analytics.migration.enabled is false",
				nil, "state", m.state.State)
			return
		}
		held, err := m.locks.Obtain(ctx, migrationLockKey, migrationLockTTL)
		if err != nil {
			if errors.Is(err, redislock.ErrNotObtained) {
				if !waiting {
					m.log.infow("analytics migration: another node holds the legacy migration")
					waiting = true
				}
			} else {
				m.log.warnw("analytics migration: cannot obtain the migration lock", err)
			}
			if !sleep(ctx, m.lockRetry) {
				return
			}
			continue
		}
		err = m.run(ctx, held)
		m.release(held)
		if !errors.Is(err, errLockLost) {
			return
		}
	}
}

func (m *migrator) release(held lease) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = held.Release(ctx)
}

func (m *migrator) load(ctx context.Context) (bool, error) {
	var state schema.SchemaMetaMigration
	err := m.db.Collection(schema.CollectionSchemaMeta).FindOne(ctx,
		bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}}).Decode(&state)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state.Checkpoint == nil {
		state.Checkpoint = &schema.MigrationPoint{Phase: phaseEvents}
	}
	m.state = state
	return true, nil
}

func (m *migrator) persist(ctx context.Context) error {
	m.state.ID = schema.SchemaMetaMigrationID
	_, err := m.db.Collection(schema.CollectionSchemaMeta).ReplaceOne(ctx,
		bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}}, m.state, options.Replace().SetUpsert(true))
	return err
}

// run executes the remaining phases from the checkpoint.
func (m *migrator) run(ctx context.Context, held lease) error {
	started := m.now()
	if m.state.StartedAt == nil {
		m.state.StartedAt = &started
	}
	m.state.State = migrationRunning
	m.state.NodeID = m.nodeID
	m.state.Error = ""
	m.state.FinishedAt = nil
	if err := m.persist(ctx); err != nil {
		return m.fail(ctx, err)
	}
	m.metrics.setState(migrationRunning)
	m.progressAt, m.progressFrom = m.now(), m.state.Processed
	m.log.infow("analytics migration: running", "phase", m.state.Checkpoint.Phase, "processed", m.state.Processed,
		"total", m.state.Total)

	for {
		var err error
		next := ""
		switch m.state.Checkpoint.Phase {
		case phaseEvents:
			err = m.withRetries(ctx, func() error { return m.replay(ctx, held, legacy.EventTimestamp) })
			next = phaseStats
		case phaseStats:
			err = m.withRetries(ctx, func() error { return m.replay(ctx, held, legacy.StatTimestamp) })
			next = phaseReconcile
		case phaseReconcile:
			err = m.withRetries(ctx, func() error { return m.reconcile(ctx) })
			next = phaseCleanup
		case phaseCleanup:
			if err = m.withRetries(ctx, func() error { return m.cleanup(ctx) }); err == nil {
				return nil
			}
		default:
			err = fmt.Errorf("unknown migration phase %q", m.state.Checkpoint.Phase)
		}
		if errors.Is(err, errLockLost) || ctx.Err() != nil {
			return errLockLost
		}
		if err != nil {
			return m.fail(ctx, err)
		}
		m.state.Checkpoint.Phase = next
		if err := m.persist(ctx); err != nil {
			return m.fail(ctx, err)
		}
		m.log.infow("analytics migration: phase completed", "next", next, "processed", m.state.Processed)
	}
}

// withRetries retries a phase that failed with a transient error, with exponential backoff; a phase that made progress
// since its last failure starts counting again.
func (m *migrator) withRetries(ctx context.Context, phase func() error) error {
	for attempt := 1; ; attempt++ {
		processed := m.state.Processed
		err := phase()
		if err == nil || errors.Is(err, errLockLost) || !isTransient(err) || ctx.Err() != nil {
			return err
		}
		if m.state.Processed > processed {
			attempt = 1
		}
		if attempt >= migrationRetries {
			return err
		}
		m.log.warnw("analytics migration: batch failed, retrying", err, "attempt", attempt)
		if !sleep(ctx, m.backoff<<(attempt-1)) {
			return err
		}
	}
}

func (m *migrator) fail(ctx context.Context, cause error) error {
	m.state.State = migrationFailed
	m.state.Error = fmt.Sprintf("%v (phase %s, events after second %d, stats after second %d)", cause,
		m.state.Checkpoint.Phase, m.state.Checkpoint.Events, m.state.Checkpoint.Stats)
	finished := m.now()
	m.state.FinishedAt = &finished
	if err := m.persist(context.WithoutCancel(ctx)); err != nil {
		m.log.warnw("analytics migration: cannot record the failure", err)
	}
	m.metrics.setState(migrationFailed)
	m.log.warnw("analytics migration: failed, the next start retries it from its checkpoint", cause,
		"processed", m.state.Processed, "total", m.state.Total)
	return cause
}

// replay reads a legacy collection after its checkpoint in whole-second batches and applies each batch, persisting
// the checkpoint after it.
func (m *migrator) replay(ctx context.Context, held lease, field string) error {
	collection, checkpoint := LegacyEventsCollection, &m.state.Checkpoint.Events
	if field == legacy.StatTimestamp {
		collection, checkpoint = legacyStatsCollection, &m.state.Checkpoint.Stats
	}
	source, err := legacy.OpenCursor(ctx, m.db.Collection(collection), field, *checkpoint, m.settings.BatchSize)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close(context.WithoutCancel(ctx)) }()
	reader := legacy.NewReader(source, field, m.settings.BatchSize)
	for {
		batch, err := reader.Next(ctx)
		if err != nil {
			return err
		}
		if len(batch.Documents) == 0 {
			return nil
		}
		started := time.Now()
		if field == legacy.EventTimestamp {
			err = m.applyEvents(ctx, batch.Documents)
		} else {
			err = m.applyStats(ctx, batch.Documents)
		}
		if err != nil {
			return err
		}
		*checkpoint = batch.Second
		m.state.Processed += int64(len(batch.Documents))
		if err := m.persist(ctx); err != nil {
			return err
		}
		if err := held.Refresh(ctx, migrationLockTTL); err != nil {
			return errLockLost
		}
		elapsed := time.Since(started)
		m.metrics.processed.WithLabelValues(collection).Add(float64(len(batch.Documents)))
		m.metrics.batchDuration.Observe(elapsed.Seconds())
		m.logProgress()
		if !sleep(ctx, legacy.Pause(len(batch.Documents), m.settings.Rate, elapsed)) {
			return ctx.Err()
		}
	}
}

// applyEvents projects the authentic events of a batch and closes the entities of its fixer events through the
// reconciler. Transient errors are retried on the same entries, which keep the steps already applied.
func (m *migrator) applyEvents(ctx context.Context, documents []bson.Raw) error {
	var entries []*eventEntry
	var fakes []*livekit.AnalyticsEvent
	for _, raw := range documents {
		event, err := legacy.DecodeEvent(raw)
		if err != nil {
			m.skip(err)
			continue
		}
		switch {
		case event.Fake:
			fakes = append(fakes, event.Message)
		case !projection.Supported(event.Message.GetType()):
			m.state.Counts.Skipped++
		default:
			entries = append(entries, &eventEntry{event: event.Message})
		}
	}
	if len(entries) > 0 {
		if err := m.retrying(ctx, func() error { return m.processor.apply(ctx, entries, nil) }); err != nil {
			return err
		}
		m.state.Counts.Events += int64(len(entries))
	}
	if len(fakes) == 0 {
		return nil
	}
	closed, skipped, err := m.closeFakes(ctx, fakes)
	m.state.Counts.Synthetic += closed
	m.state.Counts.Skipped += skipped
	return err
}

// applyStats replays the stats of a batch, one entry per second, as the server flushes them.
func (m *migrator) applyStats(ctx context.Context, documents []bson.Raw) error {
	var entries []*statsEntry
	bySecond := map[int64]*statsEntry{}
	decoded := 0
	for _, raw := range documents {
		stat, err := legacy.DecodeStat(raw)
		if err != nil {
			m.skip(err)
			continue
		}
		second := legacy.Seconds(raw, legacy.StatTimestamp)
		entry, ok := bySecond[second]
		if !ok {
			entry = &statsEntry{}
			bySecond[second] = entry
			entries = append(entries, entry)
		}
		entry.stats = append(entry.stats, stat)
		decoded++
	}
	if len(entries) == 0 {
		return nil
	}
	if err := m.retrying(ctx, func() error { return m.processor.applyStats(ctx, entries) }); err != nil {
		return err
	}
	m.state.Counts.Stats += int64(decoded)
	return nil
}

// retrying retries step on transient errors with the migration's backoff.
func (m *migrator) retrying(ctx context.Context, step func() error) error {
	for attempt := 1; ; attempt++ {
		err := step()
		if err == nil || !isTransient(err) || attempt >= migrationRetries || !sleep(ctx, m.backoff<<(attempt-1)) {
			return err
		}
	}
}

func (m *migrator) skip(err error) {
	m.state.Counts.Skipped++
	var skip *legacy.SkipError
	if errors.As(err, &skip) {
		m.log.warnw("analytics migration: legacy document skipped", nil, "id", skip.ID, "reason", skip.Reason)
		return
	}
	m.log.warnw("analytics migration: legacy document skipped", err)
}

// closeFakes closes, at the time of each fixer event, the entity it claimed closed, as the reconciler does; entities
// already closed (by an authentic event replayed before) are left alone. It returns the closures and the fixer events
// of a type the fixer never wrote.
func (m *migrator) closeFakes(ctx context.Context, fakes []*livekit.AnalyticsEvent) (int64, int64, error) {
	ids := map[livekit.AnalyticsEventType][]string{}
	for _, fake := range fakes {
		ids[fake.GetType()] = append(ids[fake.GetType()], projection.ClosedEntity(fake))
	}
	participants, err := m.store.OpenParticipantsByID(ctx, ids[livekit.AnalyticsEventType_PARTICIPANT_LEFT])
	if err != nil {
		return 0, 0, err
	}
	rooms, err := m.store.OpenRoomsByID(ctx, ids[livekit.AnalyticsEventType_ROOM_ENDED])
	if err != nil {
		return 0, 0, err
	}
	egresses, err := m.store.OpenEgressesByID(ctx, ids[livekit.AnalyticsEventType_EGRESS_ENDED])
	if err != nil {
		return 0, 0, err
	}
	sessions, err := m.store.OpenIngressSessionsByID(ctx, ids[livekit.AnalyticsEventType_INGRESS_ENDED])
	if err != nil {
		return 0, 0, err
	}
	var roomSids []string
	for _, egress := range egresses {
		roomSids = append(roomSids, egress.RoomSID)
	}
	for _, session := range sessions {
		roomSids = append(roomSids, session.RoomSID)
	}
	roomRefs, err := m.store.Rooms(ctx, roomSids)
	if err != nil {
		return 0, 0, err
	}

	participantIndex := map[string]store.OpenParticipant{}
	for _, participant := range participants {
		participantIndex[participant.SID] = participant
	}
	roomIndex := map[string]store.OpenRoom{}
	for _, room := range rooms {
		roomIndex[room.SID] = room
	}
	egressIndex := map[string]store.OpenEgress{}
	for _, egress := range egresses {
		egressIndex[egress.ID] = egress
	}
	sessionIndex := map[string]store.OpenIngressSession{}
	for _, session := range sessions {
		sessionIndex[session.ID] = session
	}

	report := &runReport{}
	c := &closer{r: m.reconciler, report: report, remaining: math.MaxInt32, deadline: time.Unix(math.MaxInt32, 0)}
	var skipped int64
	for _, fake := range fakes {
		at := schema.FromTimestamp(fake.GetTimestamp())
		id := projection.ClosedEntity(fake)
		var err error
		switch fake.GetType() {
		case livekit.AnalyticsEventType_PARTICIPANT_LEFT:
			if participant, ok := participantIndex[id]; ok {
				err = c.closeParticipants(ctx, []confirmedParticipant{{OpenParticipant: participant, reason: reasonLegacyFixer, at: at}})
			}
		case livekit.AnalyticsEventType_ROOM_ENDED:
			if room, ok := roomIndex[id]; ok {
				err = c.closeRoom(ctx, confirmedRoom{OpenRoom: room, reason: reasonLegacyFixer, at: at})
			}
		case livekit.AnalyticsEventType_EGRESS_ENDED:
			if egress, ok := egressIndex[id]; ok {
				err = c.closeEgress(ctx, egressCandidate{egress: egress, room: roomRefs[egress.RoomSID], reason: reasonLegacyFixer, at: at})
			}
		case livekit.AnalyticsEventType_INGRESS_ENDED:
			if session, ok := sessionIndex[id]; ok {
				err = c.closeIngressSession(ctx, ingressSessionCandidate{session: session, room: roomRefs[session.RoomSID],
					reason: reasonLegacyFixer, at: at})
			}
		default:
			skipped++
		}
		if err != nil {
			return 0, skipped, errors.Join(err, c.apply(ctx))
		}
	}
	if err := c.apply(ctx); err != nil {
		return 0, skipped, err
	}
	closed := report.closed
	return int64(closed.Participant + closed.Room + closed.Egress + closed.IngressSession), skipped, nil
}

// migrationReconcileRuns bounds the reconciler runs of the reconcile phase.
const migrationReconcileRuns = 20

// reconcile runs the reconciler until a run closes nothing, so that the entities the legacy writer left open and LiveKit
// no longer knows are closed: rooms become candidates once the sessions of an earlier run are closed. Follow-up runs
// are continuations, which do not count as consecutive runs for candidates of alive nodes.
func (m *migrator) reconcile(ctx context.Context) error {
	report := m.reconciler.reconcile(ctx, false)
	for runs := 1; report.err == nil && ctx.Err() == nil && runs < migrationReconcileRuns; runs++ {
		if !report.partial && report.closed == (schema.ReconcilerClosures{}) {
			break
		}
		report = m.reconciler.reconcile(ctx, true)
	}
	return report.err
}

// cleanup drops the bookkeeping collections of the legacy writer, records until when its data stays and marks the
// migration completed. legacy_events and stats expire by their own TTL.
func (m *migrator) cleanup(ctx context.Context) error {
	for _, name := range []string{legacyActiveEntitiesCollection, legacyLastAliveCollection} {
		if err := m.db.Collection(name).Drop(ctx); err != nil {
			return err
		}
	}
	var retainedUntil *time.Time
	for _, name := range []string{LegacyEventsCollection, legacyStatsCollection} {
		var latest struct {
			ExpireAt time.Time `bson:"openvidu_expire_at"`
		}
		err := m.db.Collection(name).FindOne(ctx, bson.D{{Key: "openvidu_expire_at", Value: bson.D{{Key: "$exists", Value: true}}}},
			options.FindOne().SetSort(bson.D{{Key: "openvidu_expire_at", Value: -1}}).
				SetProjection(bson.D{{Key: "openvidu_expire_at", Value: 1}})).Decode(&latest)
		if errors.Is(err, mongo.ErrNoDocuments) {
			continue
		}
		if err != nil {
			return err
		}
		if expireAt := latest.ExpireAt.UTC(); retainedUntil == nil || expireAt.After(*retainedUntil) {
			retainedUntil = &expireAt
		}
	}
	finished := m.now()
	m.state.State = migrationCompleted
	m.state.FinishedAt = &finished
	m.state.LegacyRetainedUntil = retainedUntil
	if err := m.persist(ctx); err != nil {
		return err
	}
	m.metrics.setState(migrationCompleted)
	m.log.infow("analytics migration: completed", "processed", m.state.Processed, "counts", m.state.Counts,
		"duration", finished.Sub(*m.state.StartedAt).String(), "legacyRetainedUntil", retainedUntil)
	return nil
}

// logProgress writes a progress line at most every migrationProgressInterval.
func (m *migrator) logProgress() {
	now := m.now()
	elapsed := now.Sub(m.progressAt)
	if elapsed < migrationProgressInterval {
		return
	}
	rate := float64(m.state.Processed-m.progressFrom) / elapsed.Seconds()
	fields := []any{"processed", m.state.Processed, "total", m.state.Total, "docsPerSecond", math.Round(rate)}
	if remaining := m.state.Total - m.state.Processed; rate > 0 && remaining > 0 {
		fields = append(fields, "eta", (time.Duration(float64(remaining)/rate) * time.Second).Round(time.Second).String())
	}
	m.log.infow("analytics migration: progress", fields...)
	m.progressAt, m.progressFrom = now, m.state.Processed
}
