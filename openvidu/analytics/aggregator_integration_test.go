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
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	mongotest "github.com/livekit/livekit-server/openvidu/analytics/testutil"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
	"github.com/livekit/livekit-server/pkg/config"
)

// aggregatorEnv is a reconciler environment with an aggregator over the same database and clock.
type aggregatorEnv struct {
	*reconcilerEnv
	a          *aggregator
	aggLogs    *capturedLog
	aggMetrics *aggregatorMetrics
}

func newAggregatorEnv(t *testing.T, client *mongo.Client, configure ...func(*openviduconfig.AnalyticsConfig)) *aggregatorEnv {
	t.Helper()
	env := &aggregatorEnv{reconcilerEnv: newReconcilerEnv(t, client, configure...), aggLogs: &capturedLog{},
		aggMetrics: buildAggregatorMetrics()}
	env.a = newAggregator(store.New(env.db), aggregatorSettings{interval: time.Minute,
		entitiesRetention: env.settings.Retention.Entities}, env.aggMetrics)
	env.a.log = env.aggLogs.sink()
	env.a.now = func() time.Time { return env.now }
	return env
}

func (e *aggregatorEnv) aggregate() {
	e.t.Helper()
	require.NoError(e.t, e.a.run(context.Background()))
}

// labelled records a heartbeat of node with a media node label and batch interval at.
func (e *aggregatorEnv) labelled(node string, label string, batchInterval time.Duration, at time.Time) {
	e.t.Helper()
	require.NoError(e.t, store.New(e.db).Heartbeat(context.Background(), node, store.NodeHeartbeat{ServerVersion: "test",
		BatchInterval: batchInterval, MediaStats: true, RTCEngine: "pion", MediaNode: label}, at))
}

// rollup reads a rollup document by scope and bucket; nil when it does not exist.
func (e *aggregatorEnv) rollup(collection string, scope string, bucket time.Time) bson.M {
	e.t.Helper()
	var document bson.M
	err := e.db.Collection(collection).FindOne(context.Background(),
		bson.D{{Key: "_id", Value: scope + ":" + bucket.UTC().Format(time.RFC3339)}}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	require.NoError(e.t, err)
	return document
}

// valueAt reads a dotted path of a decoded document; nil when absent.
func valueAt(document any, path string) any {
	for _, key := range strings.Split(path, ".") {
		switch d := document.(type) {
		case bson.M:
			document = d[key]
		case bson.D:
			document = nil
			for _, element := range d {
				if element.Key == key {
					document = element.Value
				}
			}
		default:
			return nil
		}
	}
	return document
}

func peaksOf(document bson.M) [2]any {
	return [2]any{valueAt(document, "rooms.peakActive"), valueAt(document, "participants.peakActive")}
}

func TestAggregatorActiveCounts(t *testing.T) {
	client := mongotest.StartMongo(t)
	ctx := context.Background()
	env := newAggregatorEnv(t, client)

	env.room("RM_a1", "ND_a", before(time.Hour))
	env.room("RM_a2", "ND_a", before(time.Hour))
	env.room("RM_b1", "ND_b", before(time.Hour))
	env.room("RM_gone", "ND_a", before(2*time.Hour))
	env.apply([]*livekit.AnalyticsEvent{on("ND_a", env.ev.roomEnded("RM_gone", "room-RM_gone", before(2*time.Hour),
		before(time.Hour), livekit.RoomEndReason_ROOM_END_API_DELETE))})
	env.testEnv.apply([]*livekit.AnalyticsEvent{env.ev.roomCreated("RM_x", "room-RM_x", before(time.Hour))})

	env.session("RM_a1", "ND_a", "PA_1", livekit.ParticipantInfo_STANDARD, before(30*time.Minute), false)
	env.session("RM_a2", "ND_a", "PA_2", livekit.ParticipantInfo_STANDARD, before(30*time.Minute), false)
	env.session("RM_a1", "ND_a", "PA_joining", livekit.ParticipantInfo_STANDARD, before(30*time.Minute), true)
	env.session("RM_a1", "ND_a", "PA_rec", livekit.ParticipantInfo_EGRESS, before(30*time.Minute), false)
	env.session("RM_b1", "ND_b", "PA_b", livekit.ParticipantInfo_STANDARD, before(30*time.Minute), false)
	left := env.session("RM_b1", "ND_b", "PA_left", livekit.ParticipantInfo_STANDARD, before(30*time.Minute), false)
	env.apply([]*livekit.AnalyticsEvent{on("ND_b", env.ev.left("RM_b1", left, before(20*time.Minute),
		livekit.DisconnectReason_CLIENT_INITIATED))})
	unlabelled := participantInfo("PA_x", "id-PA_x", livekit.ParticipantInfo_STANDARD, before(30*time.Minute))
	env.testEnv.apply([]*livekit.AnalyticsEvent{env.ev.joined("RM_x", unlabelled, before(30*time.Minute)),
		env.ev.active("RM_x", unlabelled, before(30*time.Minute-time.Second))})

	s := store.New(env.db)
	rooms, err := s.ActiveRooms(ctx)
	require.NoError(t, err)
	require.Equal(t, store.ActiveCount{Total: 4, ByNode: map[string]int32{"mn-a": 2, "mn-b": 1}}, rooms)
	participants, err := s.ActiveParticipants(ctx)
	require.NoError(t, err)
	require.Equal(t, store.ActiveCount{Total: 4, ByNode: map[string]int32{"mn-a": 2, "mn-b": 1}}, participants)
}

func TestAggregatorLiveMediaNodes(t *testing.T) {
	client := mongotest.StartMongo(t)
	env := newAggregatorEnv(t, client)
	env.labelled("ND_live", "mn-live", 10*time.Second, before(5*time.Second))
	env.labelled("ND_live_restarted", "mn-live", 10*time.Second, before(20*time.Second))
	env.labelled("ND_stopped", "mn-stopped", 10*time.Second, before(5*time.Second))
	require.NoError(t, store.New(env.db).Stopped(context.Background(), "ND_stopped", before(4*time.Second)))
	env.labelled("ND_silent", "mn-silent", 10*time.Second, before(61*time.Second))
	// A 50 s batch interval keeps a node alive for 115 s.
	env.labelled("ND_slow", "mn-slow", 50*time.Second, before(100*time.Second))
	env.labelled("ND_unlabelled", "", 10*time.Second, before(5*time.Second))

	labels, err := store.New(env.db).LiveMediaNodes(context.Background(), reconcileNow)
	require.NoError(t, err)
	require.Equal(t, []string{"mn-live", "mn-slow"}, labels)
}

func TestAggregatorQueryPlans(t *testing.T) {
	client := mongotest.StartMongo(t)
	env := newAggregatorEnv(t, client)
	for r := range 20 {
		room := fmt.Sprintf("RM_%03d", r)
		env.room(room, nodeAlive, before(3*time.Hour))
		var batch []*livekit.AnalyticsEvent
		for p := range 20 {
			info := participantInfo(fmt.Sprintf("PA_%03d_%02d", r, p), fmt.Sprintf("user-%d", p),
				livekit.ParticipantInfo_STANDARD, before(2*time.Hour))
			batch = append(batch, on(nodeAlive, env.ev.joined(room, info, before(2*time.Hour))),
				on(nodeAlive, env.ev.active(room, info, before(2*time.Hour-time.Second))))
			if p >= 5 {
				batch = append(batch, on(nodeAlive, env.ev.left(room, info, before(time.Hour),
					livekit.DisconnectReason_CLIENT_INITIATED)))
			}
		}
		if r >= 5 {
			batch = append(batch, on(nodeAlive, env.ev.roomEnded(room, "room-"+room, before(3*time.Hour), before(50*time.Minute),
				livekit.RoomEndReason_ROOM_END_API_DELETE)))
		}
		env.apply(batch)
	}

	for _, aggregation := range []store.Aggregation{store.ActiveRoomsAggregation(), store.ActiveParticipantsAggregation()} {
		plan := explain(t, env.db, bson.D{{Key: "aggregate", Value: aggregation.Collection},
			{Key: "pipeline", Value: aggregation.Pipeline}, {Key: "hint", Value: aggregation.Hint},
			{Key: "cursor", Value: bson.D{}}})
		require.Contains(t, plan.indexes, aggregation.Hint)
		require.NotContains(t, plan.stages, "COLLSCAN")
		active := count(env.testEnv, aggregation.Collection, aggregation.Pipeline[0][0].Value.(bson.D))
		require.Positive(t, active)
		require.Equal(t, active, plan.docsExamined, "%s: only the active entities are read", aggregation.Collection)
	}

	aggregate := explain(t, env.db, bson.D{{Key: "aggregate", Value: schema.CollectionParticipants},
		{Key: "pipeline", Value: store.UniqueIdentitiesPipeline(before(24*time.Hour), reconcileNow)},
		{Key: "cursor", Value: bson.D{}}})
	require.Contains(t, aggregate.indexes, "kind_status_joinedAt_id")
	require.NotContains(t, aggregate.stages, "COLLSCAN")
}

func TestAggregatorPeaksInTheRollups(t *testing.T) {
	client := mongotest.StartMongo(t)
	env := newAggregatorEnv(t, client)
	hour := reconcileNow
	sampled := hour.Add(2 * time.Minute)
	env.labelled("ND_a", "mn-a", 10*time.Second, sampled.Add(-5*time.Second))
	env.labelled("ND_idle", "mn-idle", 10*time.Second, sampled.Add(-5*time.Second))
	env.labelled("ND_stopped", "mn-stopped", 10*time.Second, sampled.Add(-5*time.Second))
	require.NoError(t, store.New(env.db).Stopped(context.Background(), "ND_stopped", sampled.Add(-4*time.Second)))
	env.room("RM_a1", "ND_a", before(time.Hour))
	env.room("RM_a2", "ND_a", before(time.Hour))
	for _, sid := range []string{"PA_1", "PA_2", "PA_3"} {
		env.session("RM_a1", "ND_a", sid, livekit.ParticipantInfo_STANDARD, before(30*time.Minute), false)
	}
	// Written by the server in the bucket of the samples: its counters and media statistics mark stay as they are.
	env.room("RM_a3", "ND_a", hour.Add(time.Minute))

	env.now = sampled
	env.aggregate()
	for _, check := range []struct {
		collection string
		bucket     time.Time
	}{
		{schema.CollectionAnalytics5Min, hour},
		{schema.CollectionAnalyticsHourly, hour},
		{schema.CollectionAnalyticsDaily, hour.Truncate(24 * time.Hour)},
	} {
		require.Equal(t, [2]any{int32(3), int32(3)}, peaksOf(env.rollup(check.collection, "global", check.bucket)), check.collection)
		require.Equal(t, [2]any{int32(3), int32(3)}, peaksOf(env.rollup(check.collection, "node:mn-a", check.bucket)), check.collection)
		require.Equal(t, [2]any{int32(0), int32(0)}, peaksOf(env.rollup(check.collection, "node:mn-idle", check.bucket)), check.collection)
		require.Nil(t, env.rollup(check.collection, "node:mn-stopped", check.bucket), check.collection)
	}
	global := env.rollup(schema.CollectionAnalytics5Min, "global", hour)
	require.Equal(t, true, valueAt(global, "mediaStats.reported"))
	require.Nil(t, valueAt(global, "mediaStats.missing"))
	require.Equal(t, int32(1), valueAt(global, "rooms.created"))
	idle := env.rollup(schema.CollectionAnalytics5Min, "node:mn-idle", hour)
	require.ElementsMatch(t, []string{"_id", "scope", "bucket", "rooms", "participants"}, keysOf(idle))

	for _, room := range []string{"RM_a2", "RM_a3"} {
		env.apply([]*livekit.AnalyticsEvent{on("ND_a", env.ev.roomEnded(room, "room-"+room, before(time.Hour),
			hour.Add(150*time.Second), livekit.RoomEndReason_ROOM_END_API_DELETE))})
	}
	env.now = hour.Add(3 * time.Minute)
	env.aggregate()
	require.Equal(t, [2]any{int32(3), int32(3)}, peaksOf(env.rollup(schema.CollectionAnalytics5Min, "global", hour)),
		"the peak is kept")
	first := snapshot(env.testEnv, schema.CollectionAnalytics5Min, schema.CollectionAnalyticsHourly, schema.CollectionAnalyticsDaily)
	env.aggregate()
	require.Equal(t, first, snapshot(env.testEnv, schema.CollectionAnalytics5Min, schema.CollectionAnalyticsHourly,
		schema.CollectionAnalyticsDaily), "a repeated run changes nothing")
}

func keysOf(document bson.M) []string {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	return keys
}

func TestAggregatorStartupRun(t *testing.T) {
	client := mongotest.StartMongo(t)
	env := newAggregatorEnv(t, client)
	env.room("RM_1", nodeAlive, before(time.Hour))
	env.room("RM_2", nodeAlive, before(time.Hour))
	env.a.settings.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.a.loop(ctx, &localLocker{})
	}()
	require.Eventually(t, func() bool {
		return peaksOf(env.rollup(schema.CollectionAnalytics5Min, "global", reconcileNow))[0] == int32(2)
	}, 5*time.Second, 20*time.Millisecond, "the first run does not wait for the interval")
	cancel()
	<-done
}

func TestAggregatorWithTheReconcilerDisabled(t *testing.T) {
	client := mongotest.StartMongo(t)
	env := newAggregatorEnv(t, client, func(c *openviduconfig.AnalyticsConfig) { c.Reconciler.Enabled = new(bool) })
	reconcilers, err := newReconcilerRunner(&config.Config{}, env.settings, env.db, env.live, Node{ID: "ND_a"})
	require.NoError(t, err)
	require.Nil(t, reconcilers)
	runner, err := newAggregatorRunner(&config.Config{}, env.settings, env.db)
	require.NoError(t, err)
	runner.aggregator.now = func() time.Time { return env.now }

	yesterday := reconcileNow.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	env.room("RM_old", nodeAlive, yesterday.Add(9*time.Hour))
	env.session("RM_old", nodeAlive, "PA_old", livekit.ParticipantInfo_STANDARD, yesterday.Add(9*time.Hour), false)
	env.room("RM_now", nodeAlive, before(time.Hour))

	require.NoError(t, runner.aggregator.run(context.Background()))
	require.Equal(t, int32(2), peaksOf(env.rollup(schema.CollectionAnalytics5Min, "global", reconcileNow))[0],
		"the open rooms of both days are active")
	require.Equal(t, int32(1), env.rollup(schema.CollectionAnalyticsDaily, "global", yesterday)["uniqueIdentities"])
}

func TestAggregatorDailyUniqueIdentitiesOfAnIdleDay(t *testing.T) {
	client := mongotest.StartMongo(t)
	env := newAggregatorEnv(t, client)
	yesterday := reconcileNow.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	env.now = yesterday.Add(10 * time.Hour)
	env.aggregate()
	idle := env.rollup(schema.CollectionAnalyticsDaily, "global", yesterday)
	require.Equal(t, [2]any{int32(0), int32(0)}, peaksOf(idle))
	require.NotContains(t, idle, "uniqueIdentities")

	env.now = reconcileNow
	env.aggregate()
	require.Equal(t, int32(0), env.rollup(schema.CollectionAnalyticsDaily, "global", yesterday)["uniqueIdentities"])
}

func TestAggregatorDailyUniqueIdentities(t *testing.T) {
	client := mongotest.StartMongo(t)
	env := newAggregatorEnv(t, client)
	day1 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	joinAs := func(kind livekit.ParticipantInfo_Kind, room string, node string, sid string, identity string, at time.Time) {
		info := participantInfo(sid, identity, kind, at)
		env.apply([]*livekit.AnalyticsEvent{on(node, env.ev.joined(room, info, at)),
			on(node, env.ev.left(room, info, at.Add(time.Minute), livekit.DisconnectReason_CLIENT_INITIATED))})
	}
	join := func(room string, node string, sid string, identity string, at time.Time) {
		joinAs(livekit.ParticipantInfo_STANDARD, room, node, sid, identity, at)
	}
	env.room("RM_a1", "ND_a", day1)
	env.room("RM_b1", "ND_b", day1)
	join("RM_a1", "ND_a", "PA_1", "alice", day1)
	joinAs(livekit.ParticipantInfo_INGRESS, "RM_a1", "ND_a", "PA_cam", "cam-1", day1)
	joinAs(livekit.ParticipantInfo_EGRESS, "RM_b1", "ND_b", "PA_rec", "EG_1", day1)
	join("RM_a1", "ND_a", "PA_2", "bob", day1.Add(time.Hour))
	join("RM_b1", "ND_b", "PA_3", "bob", day1.Add(2*time.Hour))
	join("RM_b1", "ND_b", "PA_4", "carol", day1.Add(3*time.Hour))
	env.room("RM_a2", "ND_a", day2)
	env.room("RM_b2", "ND_b", day2)
	join("RM_a2", "ND_a", "PA_5", "alice", day2)
	env.room("RM_today", "ND_a", reconcileNow.Add(-time.Hour))
	join("RM_today", "ND_a", "PA_6", "dave", reconcileNow.Add(-time.Hour))

	env.aggregate()
	value := func(scope string, day time.Time) any {
		document := find[bson.M](env.testEnv, schema.CollectionAnalyticsDaily, scope+":"+day.Truncate(24*time.Hour).Format(time.RFC3339))
		return document["uniqueIdentities"]
	}
	require.Equal(t, []any{int32(3), int32(2), int32(2)}, []any{value("global", day1), value("node:mn-a", day1), value("node:mn-b", day1)})
	require.Equal(t, []any{int32(1), int32(1), int32(0)}, []any{value("global", day2), value("node:mn-a", day2), value("node:mn-b", day2)})
	require.Nil(t, value("global", reconcileNow), "the current day has no value until it closes")

	first := snapshot(env.testEnv, schema.CollectionAnalyticsDaily)
	env.aggregate()
	require.Equal(t, first, snapshot(env.testEnv, schema.CollectionAnalyticsDaily))
	require.NotEmpty(t, env.aggLogs.matching("debug", "unique identities"))
}

func TestAggregatorDailyUniqueIdentitiesWithoutLabels(t *testing.T) {
	client := mongotest.StartMongo(t)
	env := newAggregatorEnv(t, client)
	day := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	for i, identity := range []string{"alice", "bob"} {
		info := participantInfo(fmt.Sprintf("PA_%d", i), identity, livekit.ParticipantInfo_STANDARD, day)
		env.testEnv.apply([]*livekit.AnalyticsEvent{env.ev.roomCreated("RM_x", "room-x", day), env.ev.joined("RM_x", info, day),
			env.ev.left("RM_x", info, day.Add(time.Minute), livekit.DisconnectReason_CLIENT_INITIATED)})
	}

	env.aggregate()
	global := find[bson.M](env.testEnv, schema.CollectionAnalyticsDaily, "global:"+day.Truncate(24*time.Hour).Format(time.RFC3339))
	require.Equal(t, int32(2), global["uniqueIdentities"])
	require.Zero(t, count(env.testEnv, schema.CollectionAnalyticsDaily, bson.D{{Key: "scope", Value: bson.D{{Key: "$ne", Value: "global"}}}}))
}
