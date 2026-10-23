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

//go:build integration

package analytics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	legacy "github.com/livekit/livekit-server/openvidu/analytics/migration"
	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	mongotest "github.com/livekit/livekit-server/openvidu/analytics/testutil"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
)

// recordedLegacy reads a collection recorded from the legacy writer (migration/testdata/legacy).
func recordedLegacy(t *testing.T, collection string) []bson.Raw {
	t.Helper()
	file, err := os.Open(filepath.Join("migration", "testdata", "legacy", collection+".json"))
	require.NoError(t, err)
	defer file.Close()
	var documents []bson.Raw
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for scanner.Scan() {
		var document bson.Raw
		require.NoError(t, bson.UnmarshalExtJSON(scanner.Bytes(), true, &document))
		documents = append(documents, document)
	}
	require.NoError(t, scanner.Err())
	return documents
}

// legacyData is a database written by the legacy writer.
type legacyData struct {
	events []bson.Raw
	stats  []bson.Raw
}

func recordedData(t *testing.T) legacyData {
	return legacyData{events: recordedLegacy(t, "events"), stats: recordedLegacy(t, "stats")}
}

// without returns the data without the events matched.
func (d legacyData) without(match func(bson.Raw) bool) legacyData {
	var events []bson.Raw
	for _, event := range d.events {
		if !match(event) {
			events = append(events, event)
		}
	}
	return legacyData{events: events, stats: d.stats}
}

// seed writes the collections of the legacy writer with its indexes.
func (d legacyData) seed(t *testing.T, db *mongo.Database) {
	t.Helper()
	ctx := context.Background()
	write := func(collection string, documents []bson.Raw, secondsKey string) {
		require.NoError(t, db.CreateCollection(ctx, collection))
		_, err := db.Collection(collection).Indexes().CreateMany(ctx, []mongo.IndexModel{
			{Keys: bson.D{{Key: secondsKey, Value: 1}}},
			{Keys: bson.D{{Key: "openvidu_expire_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
		})
		require.NoError(t, err)
		if len(documents) == 0 {
			return
		}
		items := make([]any, len(documents))
		for i := range documents {
			items[i] = documents[i]
		}
		_, err = db.Collection(collection).InsertMany(ctx, items)
		require.NoError(t, err)
	}
	write(schema.CollectionEvents, d.events, "timestamp.seconds")
	write(legacyStatsCollection, d.stats, "time_stamp.seconds")
	require.NoError(t, db.CreateCollection(ctx, legacyActiveEntitiesCollection))
	_, err := db.Collection(legacyLastAliveCollection).InsertOne(ctx,
		bson.D{{Key: "_id", Value: "server"}, {Key: "last_alive", Value: bson.D{{Key: "seconds", Value: int64(1791886900)}}}})
	require.NoError(t, err)
}

// legacyFixerClose builds the close event the legacy fixer inserted for an entity: a projection of its creation event
// with the close type, an ObjectId _id, no id and an integer timestamp.
func legacyFixerClose(t *testing.T, creation bson.Raw, closeType livekit.AnalyticsEventType, seconds int64) bson.Raw {
	t.Helper()
	value := func(path ...string) any {
		raw, err := creation.LookupErr(path...)
		require.NoError(t, err, "%v", path)
		var v any
		require.NoError(t, raw.Unmarshal(&v))
		return v
	}
	document := bson.D{{Key: "_id", Value: bson.NewObjectID()}, {Key: "type", Value: closeType.String()},
		{Key: "openvidu_expire_at", Value: time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)},
		{Key: "timestamp", Value: bson.D{{Key: "seconds", Value: seconds}, {Key: "nanos", Value: int32(552394272)}}}}
	switch closeType {
	case livekit.AnalyticsEventType_ROOM_ENDED:
		document = append(document, bson.E{Key: "room", Value: bson.D{{Key: "sid", Value: value("room", "sid")},
			{Key: "name", Value: value("room", "name")}, {Key: "creation_time", Value: value("room", "creation_time")}}},
			bson.E{Key: "room_id", Value: value("room", "sid")})
	case livekit.AnalyticsEventType_EGRESS_ENDED:
		ended := seconds*1_000_000_000 + 552394272
		document = append(document, bson.E{Key: "egress_id", Value: value("egress_id")}, bson.E{Key: "egress", Value: bson.D{
			{Key: "room_id", Value: value("egress", "room_id")}, {Key: "room_name", Value: value("egress", "room_name")},
			{Key: "started_at", Value: int64(value("egress", "started_at").(float64))}, {Key: "updated_at", Value: ended},
			{Key: "ended_at", Value: ended}, {Key: "Request", Value: value("egress", "Request")},
			{Key: "status", Value: "EGRESS_COMPLETE"},
		}})
	default:
		t.Fatalf("no fixer close for %s", closeType)
	}
	raw, err := bson.Marshal(document)
	require.NoError(t, err)
	return raw
}

func legacyOfType(eventType string) func(bson.Raw) bool {
	return func(raw bson.Raw) bool {
		value, err := raw.LookupErr("type")
		return err == nil && value.StringValue() == eventType
	}
}

func lookupString(raw bson.Raw, path ...string) string {
	value, err := raw.LookupErr(path...)
	if err != nil {
		return ""
	}
	s, _ := value.StringValueOK()
	return s
}

// migrationEnv is a database seeded with legacy data, initialized by the v2 writer, with a migrator over it.
type migrationEnv struct {
	t        *testing.T
	db       *mongo.Database
	settings openviduconfig.AnalyticsSettings
	live     *fakeLive
	logs     *capturedLog
	metrics  *migrationMetrics
	m        *migrator
	pending  bool
}

func newMigrationEnv(t *testing.T, client *mongo.Client, name string, data *legacyData,
	configure ...func(*openviduconfig.AnalyticsConfig)) *migrationEnv {
	t.Helper()
	ctx := context.Background()
	db := client.Database(name)
	require.NoError(t, db.Drop(ctx))
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	if data != nil {
		data.seed(t, db)
	}
	settings := testSettings(t, func(c *openviduconfig.AnalyticsConfig) {
		c.ValidationAction = "error"
		c.Migration.BatchSize = 50
		rate := 0
		c.Migration.Rate = &rate
		for _, apply := range configure {
			apply(c)
		}
	})
	_, err := initialize(ctx, db, settings, Node{ID: "ND_migrator"})
	require.NoError(t, err)
	pending, err := prepareMigration(ctx, db, "3.10.0-test")
	require.NoError(t, err)

	env := &migrationEnv{t: t, db: db, settings: settings, live: newFakeLive(), logs: &capturedLog{},
		metrics: buildMigrationMetrics(), pending: pending}
	env.m = env.newMigrator(&localLocker{})
	return env
}

func (e *migrationEnv) newMigrator(locks locker) *migrator {
	rec := newReconciler(store.New(e.db), e.live, projectionOptions(e.settings), reconcilerSettings{
		batchInterval: e.settings.Interval, interval: e.settings.Reconciler.Interval, cleanupInterval: 3 * time.Minute,
		maxClosures: e.settings.Reconciler.MaxClosuresPerRun, entitiesRetention: e.settings.Retention.Entities,
	}, "ND_migrator", buildReconcilerMetrics())
	rec.log = e.logs.sink()
	m := newMigrator(e.db, e.settings, rec, locks, "ND_migrator", e.metrics)
	m.log = e.logs.sink()
	m.backoff = 10 * time.Millisecond
	m.lockRetry = 20 * time.Millisecond
	return m
}

func (e *migrationEnv) state() schema.SchemaMetaMigration {
	e.t.Helper()
	var state schema.SchemaMetaMigration
	require.NoError(e.t, e.db.Collection(schema.CollectionSchemaMeta).FindOne(context.Background(),
		bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}}).Decode(&state))
	return state
}

func (e *migrationEnv) run() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	e.m.loop(ctx)
}

func (e *migrationEnv) count(collection string, filter bson.D) int64 {
	e.t.Helper()
	n, err := e.db.Collection(collection).CountDocuments(context.Background(), filter)
	require.NoError(e.t, err)
	return n
}

// v2Snapshot reads every v2 collection a migration writes as maps keyed by document, without writer bookkeeping and
// the ObjectIds of track_stats, so that documents written in a different order compare equal.
func (e *migrationEnv) v2Snapshot() map[string]map[string]map[string]any {
	e.t.Helper()
	collections := append([]string{schema.CollectionTrackStats}, allCollections...)
	result := map[string]map[string]map[string]any{}
	for _, collection := range collections {
		cursor, err := e.db.Collection(collection).Find(context.Background(), bson.D{})
		require.NoError(e.t, err)
		var documents []bson.D
		require.NoError(e.t, cursor.All(context.Background(), &documents))
		byKey := map[string]map[string]any{}
		for _, document := range documents {
			normalized := normalizeDocument(document)
			key := fmt.Sprint(normalized["_id"])
			if collection == schema.CollectionTrackStats {
				delete(normalized, "_id")
				key = fmt.Sprint(normalized["ts"], normalized["meta"])
			}
			byKey[key] = normalized
		}
		result[collection] = byKey
	}
	return result
}

// secrets lists the v2 documents that carry a secret field name or one of the placeholders of the recorded secrets.
func (e *migrationEnv) secrets() []string {
	e.t.Helper()
	var found []string
	for collection, documents := range e.v2Snapshot() {
		for key, document := range documents {
			text, err := json.Marshal(document)
			require.NoError(e.t, err)
			for _, secret := range []string{"turn_password", "stream_key", "access_key", "session_token", "SECRET-"} {
				if strings.Contains(string(text), secret) {
					found = append(found, collection+" "+key+": "+secret)
				}
			}
		}
	}
	return found
}

// checkInvariants asserts the invariants of a completed migration of data.
func (e *migrationEnv) checkInvariants(data legacyData) {
	t := e.t
	t.Helper()
	rooms, participants, egresses, sessions := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	fakes := 0
	for _, raw := range data.events {
		eventType := lookupString(raw, "type")
		for _, path := range [][]string{{"room", "sid"}, {"room_id"}, {"egress", "room_id"}, {"ingress", "state", "room_id"}} {
			if sid := lookupString(raw, path...); sid != "" {
				rooms[sid] = true
			}
		}
		if strings.HasPrefix(eventType, "PARTICIPANT_") {
			participants[lookupString(raw, "participant_id")] = true
		}
		if id := lookupString(raw, "egress_id"); id != "" {
			egresses[id] = true
		}
		if id := lookupString(raw, "ingress", "state", "resource_id"); id != "" && eventType != "INGRESS_CREATED" &&
			eventType != "INGRESS_DELETED" {
			sessions[id] = true
		}
		if raw.Lookup("_id").Type == bson.TypeObjectID {
			fakes++
		}
	}
	require.EqualValues(t, len(rooms), e.count(schema.CollectionRooms, bson.D{}), "one room per legacy room sid")
	require.EqualValues(t, len(participants), e.count(schema.CollectionParticipants, bson.D{}), "one participant per session")
	require.EqualValues(t, len(egresses), e.count(schema.CollectionEgresses, bson.D{}), "one egress per egress id")
	require.Zero(t, e.count(schema.CollectionEgresses, bson.D{
		{Key: "roomName", Value: bson.D{{Key: "$exists", Value: true}}},
		{Key: "$expr", Value: bson.D{{Key: "$ne", Value: bson.A{"$roomNameLower", bson.D{{Key: "$toLower", Value: "$roomName"}}}}}},
	}), "every egress with a room name has its lower-cased search field")
	require.EqualValues(t, len(sessions), e.count(schema.CollectionIngressSessions, bson.D{}), "one session per resource id")

	require.Empty(t, e.secrets(), "no secret reaches the v2 collections")

	// Event-closed entities keep the legacy durations.
	left := map[string]bson.Raw{}
	for _, raw := range data.events {
		if legacyOfType("PARTICIPANT_LEFT")(raw) && raw.Lookup("_id").Type == bson.TypeString {
			left[lookupString(raw, "participant_id")] = raw
		}
	}
	cursor, err := e.db.Collection(schema.CollectionParticipants).Find(context.Background(), bson.D{{Key: "closedBy", Value: "event"}})
	require.NoError(t, err)
	var closed []schema.ParticipantDocument
	require.NoError(t, cursor.All(context.Background(), &closed))
	for _, participant := range closed {
		raw, ok := left[participant.ID]
		if !ok || participant.DisconnectReason == "RECONCILED" {
			continue
		}
		leftAt, err := legacy.DecodeEvent(raw)
		require.NoError(t, err)
		expected := leftAt.Message.GetTimestamp().AsTime().Sub(participant.JoinedAt)
		require.InDelta(t, expected.Milliseconds(), *participant.DurationMs, 1000, participant.ID)
	}

	state := e.state()
	require.Equal(t, migrationCompleted, state.State)
	require.LessOrEqual(t, state.Counts.Synthetic, int64(fakes))
	require.EqualValues(t, state.Counts.Synthetic, e.count(schema.CollectionEvents, bson.D{{Key: "synthetic", Value: true},
		{Key: "payload.reconciler.reason", Value: reasonLegacyFixer}}))
	require.EqualValues(t, len(data.events)+len(data.stats), state.Processed)
	require.EqualValues(t, len(data.stats), state.Counts.Stats)
}

func TestMigrationStartup(t *testing.T) {
	client := mongotest.StartMongo(t)
	ctx := context.Background()

	t.Run("legacy deployment upgraded", func(t *testing.T) {
		data := recordedData(t)
		env := newMigrationEnv(t, client, "migration_upgrade", &data)
		require.True(t, env.pending)
		require.EqualValues(t, len(data.events), env.count(LegacyEventsCollection, bson.D{}))
		indexes, err := env.db.Collection(LegacyEventsCollection).Indexes().ListSpecifications(ctx)
		require.NoError(t, err)
		var names []string
		for _, index := range indexes {
			names = append(names, index.Name)
		}
		require.Contains(t, names, "timestamp.seconds_1")
		require.Contains(t, names, "openvidu_expire_at_1")
		require.Zero(t, env.count(schema.CollectionEvents, bson.D{}), "the v2 events collection starts empty")

		state := env.state()
		require.Equal(t, migrationPending, state.State)
		require.EqualValues(t, len(data.events)+len(data.stats), state.Total)
		require.Equal(t, "3.10.0-test", state.WriterVersion)
		require.Zero(t, state.FromSchemaVersion)
		require.Equal(t, phaseEvents, state.Checkpoint.Phase)
	})

	t.Run("fresh deployment", func(t *testing.T) {
		env := newMigrationEnv(t, client, "migration_fresh", nil)
		require.False(t, env.pending)
		require.Zero(t, env.count(schema.CollectionSchemaMeta, bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}}))
	})

	t.Run("completed migration", func(t *testing.T) {
		data := legacyData{events: recordedLegacy(t, "events")[:40]}
		env := newMigrationEnv(t, client, "migration_completed", &data)
		env.run()
		before := env.state()
		require.Equal(t, migrationCompleted, before.State)
		pending, err := prepareMigration(ctx, env.db, "3.10.0-test")
		require.NoError(t, err)
		require.False(t, pending)
		env.run()
		require.Equal(t, before, env.state())
	})

	t.Run("migration disabled", func(t *testing.T) {
		data := legacyData{events: recordedLegacy(t, "events")[:40]}
		env := newMigrationEnv(t, client, "migration_disabled", &data, func(c *openviduconfig.AnalyticsConfig) {
			disabled := false
			c.Migration.Enabled = &disabled
		})
		env.run()
		require.Equal(t, migrationPending, env.state().State)
		require.Len(t, env.logs.matching("warn", "openvidu.analytics.migration.enabled"), 1)
		require.Zero(t, env.count(schema.CollectionEvents, bson.D{}))
	})

	t.Run("two nodes start together", func(t *testing.T) {
		data := legacyData{events: recordedLegacy(t, "events")[:200]}
		env := newMigrationEnv(t, client, "migration_two_nodes", &data)
		shared := &localLocker{}
		first, second := env.newMigrator(shared), env.newMigrator(shared)
		secondLogs := &capturedLog{}
		second.log = secondLogs.sink()

		held, err := shared.Obtain(ctx, migrationLockKey, migrationLockTTL)
		require.NoError(t, err)
		waiting, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		second.loop(waiting)
		cancel()
		require.Len(t, secondLogs.matching("info", "another node holds the legacy migration"), 1)
		require.Equal(t, migrationPending, env.state().State)
		require.NoError(t, held.Release(ctx))

		first.loop(ctx)
		require.Equal(t, migrationCompleted, env.state().State)
	})

	t.Run("cursor query is index-backed", func(t *testing.T) {
		data := recordedData(t)
		env := newMigrationEnv(t, client, "migration_plan", &data)
		plan := explain(t, env.db, bson.D{{Key: "find", Value: LegacyEventsCollection},
			{Key: "filter", Value: legacy.CursorFilter(legacy.EventTimestamp, 1791884000)},
			{Key: "sort", Value: bson.D{{Key: "timestamp.seconds", Value: 1}}}})
		require.Contains(t, plan.indexes, "timestamp.seconds_1")
		require.NotContains(t, plan.stages, "COLLSCAN")
	})
}

func TestMigrationReplay(t *testing.T) {
	client := mongotest.StartMongo(t)
	ctx := context.Background()

	t.Run("recorded snapshot with fixer closures", func(t *testing.T) {
		data := recordedData(t)
		roomCreated := data.events[0]
		for _, raw := range data.events {
			if legacyOfType("ROOM_CREATED")(raw) {
				roomCreated = raw
				break
			}
		}
		roomSid := lookupString(roomCreated, "room", "sid")
		var authenticEnd bson.Raw
		for _, raw := range data.events {
			if legacyOfType("ROOM_ENDED")(raw) && lookupString(raw, "room", "sid") == roomSid {
				authenticEnd = raw
			}
		}
		require.NotNil(t, authenticEnd)
		endSeconds := legacy.Seconds(authenticEnd, legacy.EventTimestamp)
		// The fixer closed the room before its authentic ROOM_ENDED was written.
		data.events = append(data.events, legacyFixerClose(t, roomCreated, livekit.AnalyticsEventType_ROOM_ENDED, endSeconds-1))

		env := newMigrationEnv(t, client, "migration_recorded", &data)
		env.run()
		env.checkInvariants(data)

		state := env.state()
		require.Positive(t, state.Counts.Synthetic)
		require.EqualValues(t, 306, state.Counts.Skipped, "the WEBHOOK events are not part of the model")
		require.EqualValues(t, len(data.events)-306-7, state.Counts.Events)
		require.NotNil(t, state.LegacyRetainedUntil)
		require.NotNil(t, state.StartedAt)
		require.NotNil(t, state.FinishedAt)
		require.Equal(t, "ND_migrator", state.NodeID)

		authentic, err := legacy.DecodeEvent(authenticEnd)
		require.NoError(t, err)
		room := find[schema.RoomDocument](&testEnv{t: t, db: env.db}, schema.CollectionRooms, roomSid)
		require.Equal(t, "event", room.ClosedBy, "the authentic ROOM_ENDED overrides the fixer's")
		require.Equal(t, strings.TrimPrefix(authentic.Message.GetRoomEndReason().String(), "ROOM_END_"), room.EndReason)
		require.True(t, schema.FromTimestamp(authentic.Message.GetTimestamp()).Equal(*room.EndedAt))

		for _, raw := range data.events {
			if raw.Lookup("_id").Type != bson.TypeObjectID || !legacyOfType("PARTICIPANT_LEFT")(raw) {
				continue
			}
			fake, err := legacy.DecodeEvent(raw)
			require.NoError(t, err)
			participant := find[schema.ParticipantDocument](&testEnv{t: t, db: env.db}, schema.CollectionParticipants,
				fake.Message.GetParticipantId())
			require.Equal(t, "left", participant.Status)
			if participant.ClosedBy == projection.ClosedByReconciler && participant.DisconnectReason == projection.Reconciled &&
				participant.LeftAt.Equal(schema.FromTimestamp(fake.Message.GetTimestamp())) {
				require.EqualValues(t, 1, env.count(schema.CollectionEvents, bson.D{
					{Key: "_id", Value: projection.SyntheticEventID(participant.ID)}, {Key: "synthetic", Value: true}}))
			}
		}

		require.Positive(t, env.count(schema.CollectionTrackStats, bson.D{}))
		require.Positive(t, env.count(schema.CollectionParticipants, bson.D{{Key: "bytes.up", Value: bson.D{{Key: "$gt", Value: 0}}}}))
		require.Zero(t, env.count(schema.CollectionParticipants, bson.D{{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{"active", "joining"}}}}}),
			"the reconcile phase closes what the legacy writer left open")
		require.Zero(t, env.count(schema.CollectionRooms, bson.D{{Key: "status", Value: "active"}}))

		names, err := env.db.ListCollectionNames(ctx, bson.D{})
		require.NoError(t, err)
		require.NotContains(t, names, legacyActiveEntitiesCollection)
		require.NotContains(t, names, legacyLastAliveCollection)
		require.EqualValues(t, len(data.events), env.count(LegacyEventsCollection, bson.D{}), "legacy events are kept")
		require.EqualValues(t, len(data.stats), env.count(legacyStatsCollection, bson.D{}), "legacy stats are kept")
		require.NotEmpty(t, env.logs.matching("info", "analytics migration: completed"))
	})

	t.Run("the secret check catches a leaked value", func(t *testing.T) {
		data := legacyData{events: recordedLegacy(t, "events")[:60]}
		for i, raw := range data.events {
			if legacyOfType("ROOM_CREATED")(raw) {
				var document bson.D
				require.NoError(t, bson.Unmarshal(raw, &document))
				for j := range document {
					if document[j].Key == "room" {
						room := document[j].Value.(bson.D)
						for k := range room {
							if room[k].Key == "name" {
								room[k].Value = "SECRET-leaked-name"
							}
						}
					}
				}
				mutated, err := bson.Marshal(document)
				require.NoError(t, err)
				data.events[i] = mutated
				break
			}
		}
		env := newMigrationEnv(t, client, "migration_secret", &data)
		env.run()
		require.NotEmpty(t, env.secrets())
	})

	t.Run("a fixer EGRESS_ENDED never makes an egress complete", func(t *testing.T) {
		recorded := recordedData(t)
		started := recorded.events[0]
		for _, raw := range recorded.events {
			if legacyOfType("EGRESS_STARTED")(raw) {
				started = raw
			}
		}
		data := recorded.without(func(raw bson.Raw) bool {
			eventType := lookupString(raw, "type")
			return (eventType == "EGRESS_ENDED" || eventType == "EGRESS_UPDATED") &&
				lookupString(raw, "egress_id") == lookupString(started, "egress_id")
		})
		data.events = append(data.events, legacyFixerClose(t, started, livekit.AnalyticsEventType_EGRESS_ENDED,
			legacy.Seconds(started, legacy.EventTimestamp)+60))
		env := newMigrationEnv(t, client, "migration_fake_egress", &data)
		env.run()
		egress := find[schema.EgressDocument](&testEnv{t: t, db: env.db}, schema.CollectionEgresses, lookupString(started, "egress_id"))
		require.Equal(t, "failed", egress.Status)
		require.Equal(t, "reconciler", egress.ClosedBy)
		require.Equal(t, projection.LostEgressError, egress.Error)
		require.NotEmpty(t, egress.RoomName)
		require.Equal(t, strings.ToLower(egress.RoomName), egress.RoomNameLower)
	})

	t.Run("live events of a room active across the upgrade converge", func(t *testing.T) {
		full := recordedData(t)
		reference := newMigrationEnv(t, client, "migration_live_reference", &full)
		reference.run()

		var roomSid string
		for _, raw := range full.events {
			if legacyOfType("ROOM_ENDED")(raw) {
				roomSid = lookupString(raw, "room", "sid")
				break
			}
		}
		closing := func(raw bson.Raw) bool {
			eventType := lookupString(raw, "type")
			return (eventType == "ROOM_ENDED" && lookupString(raw, "room", "sid") == roomSid) ||
				(eventType == "PARTICIPANT_LEFT" && lookupString(raw, "room_id") == roomSid && raw.Lookup("_id").Type == bson.TypeString)
		}
		data := full.without(closing)
		env := newMigrationEnv(t, client, "migration_live", &data)
		var liveEvents []*livekit.AnalyticsEvent
		for _, raw := range full.events {
			if closing(raw) {
				event, err := legacy.DecodeEvent(raw)
				require.NoError(t, err)
				liveEvents = append(liveEvents, event.Message)
			}
		}
		live := &testEnv{t: t, db: env.db, settings: env.settings, p: &processor{store: store.New(env.db),
			opts: projectionOptions(env.settings), cache: newRefCache()}}
		live.apply(liveEvents)
		env.run()

		expected := find[schema.RoomDocument](&testEnv{t: t, db: reference.db}, schema.CollectionRooms, roomSid)
		actual := find[schema.RoomDocument](&testEnv{t: t, db: env.db}, schema.CollectionRooms, roomSid)
		require.Equal(t, "closed", actual.Status)
		require.Equal(t, "event", actual.ClosedBy)
		require.Equal(t, expected.EndReason, actual.EndReason)
		require.True(t, expected.EndedAt.Equal(*actual.EndedAt))
		require.Equal(t, expected.Participants.Sessions, actual.Participants.Sessions)
		require.Equal(t, expected.Participants.Unique, actual.Participants.Unique)
		require.EqualValues(t, 0, actual.Participants.Active)
	})
}

// failingLease loses the lock at its n-th refresh, as when the runner's node is killed or partitioned.
type failingLease struct {
	lease
	refreshes *int
	failAt    int
	onRefresh func()
}

func (l failingLease) Refresh(ctx context.Context, ttl time.Duration) error {
	if l.onRefresh != nil {
		l.onRefresh()
	}
	*l.refreshes++
	if *l.refreshes == l.failAt {
		return errors.New("lock expired")
	}
	return l.lease.Refresh(ctx, ttl)
}

// losingLocker grants a lease that is lost after failAt batches, then normal leases.
type losingLocker struct {
	inner     localLocker
	refreshes int
	failAt    int
	granted   int
	onObtain  func()
	onRefresh func()
}

func (l *losingLocker) Obtain(ctx context.Context, key string, ttl time.Duration) (lease, error) {
	if l.onObtain != nil {
		l.onObtain()
	}
	held, err := l.inner.Obtain(ctx, key, ttl)
	if err != nil {
		return nil, err
	}
	l.granted++
	if l.granted == 1 {
		return failingLease{lease: held, refreshes: &l.refreshes, failAt: l.failAt, onRefresh: l.onRefresh}, nil
	}
	return held, nil
}

func TestMigrationResilience(t *testing.T) {
	client := mongotest.StartMongo(t)

	t.Run("resumes after a lost lock without reapplying a document", func(t *testing.T) {
		data := recordedData(t)
		reference := newMigrationEnv(t, client, "migration_resume_reference", &data)
		reference.run()

		env := newMigrationEnv(t, client, "migration_resume", &data)
		batches := (len(data.events) + len(data.stats)) / 50
		locks := &losingLocker{failAt: batches * 4 / 10}
		env.m = env.newMigrator(locks)
		env.run()

		require.Equal(t, 2, locks.granted, "the lock was obtained again after it was lost")
		require.Equal(t, migrationCompleted, env.state().State)
		require.Equal(t, reference.state().Processed, env.state().Processed)
		require.Equal(t, reference.state().Counts, env.state().Counts)
		require.Equal(t, reference.count(schema.CollectionTrackStats, bson.D{}), env.count(schema.CollectionTrackStats, bson.D{}),
			"no stats sample is duplicated")
		expected, actual := reference.v2Snapshot(), env.v2Snapshot()
		for collection := range expected {
			require.Equal(t, len(expected[collection]), len(actual[collection]), collection)
			require.Equal(t, expected[collection], actual[collection], collection)
		}
	})

	t.Run("fails after the retries and resumes on the next start", func(t *testing.T) {
		data := legacyData{events: recordedLegacy(t, "events")[:300]}
		env := newMigrationEnv(t, client, "migration_failure", &data)
		real := env.m.processor.store
		env.m.processor.store = &unavailableStore{batchStore: real}
		env.run()

		state := env.state()
		require.Equal(t, migrationFailed, state.State)
		require.Contains(t, state.Error, "phase events")
		require.NotNil(t, state.FinishedAt)
		require.Equal(t, 4.0, testutil.ToFloat64(env.metrics.state))

		env.m = env.newMigrator(&localLocker{})
		env.run()
		require.Equal(t, migrationCompleted, env.state().State)
		require.Empty(t, env.state().Error)
	})

	t.Run("state and metrics through the run", func(t *testing.T) {
		data := legacyData{events: recordedLegacy(t, "events")[:300], stats: recordedLegacy(t, "stats")[:100]}
		env := newMigrationEnv(t, client, "migration_metrics", &data)
		var states []float64
		var mu sync.Mutex
		record := func() {
			mu.Lock()
			defer mu.Unlock()
			states = append(states, testutil.ToFloat64(env.metrics.state))
		}
		locks := &losingLocker{failAt: -1, onObtain: record}
		var running []float64
		locks.onRefresh = func() { running = append(running, testutil.ToFloat64(env.metrics.state)) }
		env.m = env.newMigrator(locks)
		env.run()
		record()
		require.Equal(t, []float64{1, 3}, states)
		require.NotEmpty(t, running)
		for _, state := range running {
			require.Equal(t, 2.0, state, "running while it replays")
		}
		require.Equal(t, float64(300), counterValue(env.metrics.processed, LegacyEventsCollection))
		require.Equal(t, float64(100), counterValue(env.metrics.processed, legacyStatsCollection))
		require.Len(t, env.logs.matching("info", "analytics migration: running"), 1)
		require.Len(t, env.logs.matching("info", "analytics migration: phase completed"), 3)
		require.Len(t, env.logs.matching("info", "analytics migration: completed"), 1)
	})

	t.Run("a repeated replay of the events changes nothing", func(t *testing.T) {
		data := legacyData{events: recordedLegacy(t, "events")}
		env := newMigrationEnv(t, client, "migration_repeat", &data)
		env.run()
		first := env.v2Snapshot()
		_, err := env.db.Collection(schema.CollectionSchemaMeta).DeleteOne(context.Background(),
			bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}})
		require.NoError(t, err)
		require.NoError(t, env.db.Collection(legacyLastAliveCollection).Drop(context.Background()))
		_, err = env.db.Collection(legacyLastAliveCollection).InsertOne(context.Background(), bson.D{{Key: "_id", Value: "server"}})
		require.NoError(t, err)
		pending, err := prepareMigration(context.Background(), env.db, "3.10.0-test")
		require.NoError(t, err)
		require.True(t, pending)
		env.m = env.newMigrator(&localLocker{})
		env.run()
		require.Equal(t, first, env.v2Snapshot())
	})
}

// unavailableStore fails every entity write with a transient error, as a MongoDB without a primary does.
type unavailableStore struct {
	batchStore
}

func (s *unavailableStore) ApplyUpdates(context.Context, []projection.Update) error {
	return mongo.CommandError{Code: 91, Message: "shutdown in progress", Labels: []string{"RetryableWriteError"}}
}
