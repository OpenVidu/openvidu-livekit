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
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/livekit/livekit-server/pkg/config"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	"github.com/livekit/livekit-server/openvidu/analytics/testutil"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
)

func TestWriterLifecycle(t *testing.T) {
	client, uri := testutil.StartMongoWithURI(t)
	ctx := context.Background()

	t.Run("validator rejection in error mode drops only the rejected write", func(t *testing.T) {
		env := newEnv(t, client)
		w := newWriter(store.New(env.db), env.settings, Node{ID: testNode}, newMetrics())
		before := counterValue(w.metrics.writeErrors, schema.CollectionRooms, "rejected")

		var ev events
		nameless := ev.next(livekit.AnalyticsEventType_ROOM_CREATED, at("0s"))
		nameless.Room = &livekit.Room{Sid: "RM_nameless"}
		w.enqueueEvents([]*livekit.AnalyticsEvent{nameless, ev.roomCreated("RM_valid", "valid", at("1s"))})

		require.NoError(t, w.tick(ctx))
		require.Equal(t, int64(1), count(env, schema.CollectionRooms, bson.D{{Key: "_id", Value: "RM_valid"}}))
		require.Zero(t, count(env, schema.CollectionRooms, bson.D{{Key: "_id", Value: "RM_nameless"}}))
		require.Equal(t, float64(1), counterValue(w.metrics.writeErrors, schema.CollectionRooms, "rejected")-before)
		require.Zero(t, w.events.Len())
	})

	t.Run("heartbeat advances lastSeenAt on an idle node", func(t *testing.T) {
		env := newEnv(t, client)
		w := newWriter(store.New(env.db), env.settings, Node{ID: testNode, Region: "eu-west"}, newMetrics())

		require.NoError(t, w.tick(ctx))
		first := find[schema.NodeDocument](env, schema.CollectionNodes, testNode)
		time.Sleep(20 * time.Millisecond)
		require.NoError(t, w.tick(ctx))
		second := find[schema.NodeDocument](env, schema.CollectionNodes, testNode)

		require.Equal(t, first.FirstSeenAt, second.FirstSeenAt)
		require.True(t, second.LastSeenAt.After(first.LastSeenAt))
		require.Equal(t, "eu-west", second.Region)
		require.NotEmpty(t, second.ServerVersion)
	})

	t.Run("stop flushes the shutdown events and the final stats", func(t *testing.T) {
		name := "lifecycle_stop"
		db := client.Database(name)
		require.NoError(t, db.Drop(ctx))
		t.Cleanup(func() { _ = db.Drop(context.Background()) })
		conf := &config.Config{}
		conf.OpenVidu.Analytics = openviduconfig.AnalyticsConfig{
			Enabled: true, MongoUrl: uri, Database: name, Interval: "60s", ValidationAction: "error",
		}
		require.NoError(t, InitializeAnalytics(conf, Node{ID: testNode}, nil))
		t.Cleanup(func() {
			mu.Lock()
			instance = nil
			mu.Unlock()
		})
		go Start()

		var ev events
		alice := participantInfo("PA_alice", "alice", livekit.ParticipantInfo_STANDARD, at("1s"))
		camera := cameraTrack("TR_cam")
		eventsClient := NewOpenViduEventsIngestClient()
		statsClient := NewOpenViduStatsIngestClient()
		require.NoError(t, eventsClient.Send(&livekit.AnalyticsEvents{Events: []*livekit.AnalyticsEvent{
			ev.roomCreated("RM_1", "room", at("0s")),
			ev.joined("RM_1", alice, at("1s")),
			ev.active("RM_1", alice, at("2s")),
			ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_1", alice, camera, at("3s")),
		}}))
		require.NoError(t, statsClient.Send(&livekit.AnalyticsStats{Stats: []*livekit.AnalyticsStat{
			stat("RM_1", "PA_alice", "TR_cam", livekit.StreamType_UPSTREAM, at("10s"), 4.5, 100000),
		}}))
		// What the telemetry service flushes while the server shuts down.
		require.NoError(t, eventsClient.Send(&livekit.AnalyticsEvents{Events: []*livekit.AnalyticsEvent{
			ev.left("RM_1", alice, at("20s"), livekit.DisconnectReason_SERVER_SHUTDOWN),
			ev.roomEnded("RM_1", "room", at("0s"), at("21s"), livekit.RoomEndReason_ROOM_END_SERVER_SHUTDOWN),
		}}))
		require.NoError(t, statsClient.Send(&livekit.AnalyticsStats{Stats: []*livekit.AnalyticsStat{
			stat("RM_1", "PA_alice", "TR_cam", livekit.StreamType_UPSTREAM, at("20s"), 4, 50000),
		}}))

		stopCtx, cancel := context.WithTimeout(ctx, ShutdownTimeout)
		defer cancel()
		Stop(stopCtx)

		var room schema.RoomDocument
		require.NoError(t, db.Collection(schema.CollectionRooms).FindOne(ctx, bson.D{{Key: "_id", Value: "RM_1"}}).Decode(&room))
		require.Equal(t, "closed", room.Status)
		require.Equal(t, "SERVER_SHUTDOWN", room.EndReason)
		var participant schema.ParticipantDocument
		require.NoError(t, db.Collection(schema.CollectionParticipants).FindOne(ctx, bson.D{{Key: "_id", Value: "PA_alice"}}).Decode(&participant))
		require.Equal(t, "SERVER_SHUTDOWN", participant.DisconnectReason)
		statsCount, err := db.Collection(schema.CollectionTrackStats).CountDocuments(ctx, bson.D{})
		require.NoError(t, err)
		require.Equal(t, int64(2), statsCount)
		eventsCount, err := db.Collection(schema.CollectionEvents).CountDocuments(ctx, bson.D{})
		require.NoError(t, err)
		require.Equal(t, int64(6), eventsCount)
	})

	t.Run("legacy events collection is renamed once", func(t *testing.T) {
		db := legacyDatabase(t, client, "lifecycle_legacy")
		settings := testSettings(t, func(c *openviduconfig.AnalyticsConfig) { c.ValidationAction = "error" })

		_, err := initialize(ctx, db, settings, Node{ID: testNode})
		require.NoError(t, err)
		require.Equal(t, int64(2), countIn(t, db, LegacyEventsCollection))
		require.Zero(t, countIn(t, db, schema.CollectionEvents))
		require.True(t, hasValidator(t, db, schema.CollectionEvents), "events is the v2 collection")

		_, err = initialize(ctx, db, settings, Node{ID: testNode})
		require.NoError(t, err)
		require.Equal(t, int64(2), countIn(t, db, LegacyEventsCollection))
	})

	t.Run("legacy events and legacy_events together stop the start", func(t *testing.T) {
		db := legacyDatabase(t, client, "lifecycle_legacy_both")
		_, err := db.Collection(LegacyEventsCollection).InsertOne(ctx, bson.D{{Key: "type", Value: "roomCreated"}})
		require.NoError(t, err)

		_, err = initialize(ctx, db, testSettings(t, nil), Node{ID: testNode})
		require.ErrorContains(t, err, LegacyEventsCollection)
	})

	t.Run("nodes starting at the same time all initialize", func(t *testing.T) {
		db := legacyDatabase(t, client, "lifecycle_race")
		settings := testSettings(t, func(c *openviduconfig.AnalyticsConfig) { c.ValidationAction = "error" })

		const nodes = 4
		errs := make([]error, nodes)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range nodes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = initialize(ctx, db, settings, Node{ID: testNode})
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}
		require.Equal(t, int64(2), countIn(t, db, LegacyEventsCollection))
		require.True(t, hasValidator(t, db, schema.CollectionEvents))
	})

	// Last: it makes the server step down, which the other subtests must not see.
	t.Run("not-primary step-down is retried with the same batch", func(t *testing.T) {
		if testutil.External() {
			t.Skip("does not step down a server given through ANALYTICS_TEST_MONGO_URI")
		}
		env := newEnv(t, client)
		w := newWriter(store.New(env.db), env.settings, Node{ID: testNode}, newMetrics())
		retriesBefore := counterValue(w.metrics.batches, "retry")
		batch := roomEvents(3)
		w.enqueueEvents(batch)

		// The server closes the connections when it steps down, so the command itself may fail.
		_ = client.Database("admin").RunCommand(ctx, bson.D{
			{Key: "replSetStepDown", Value: 5}, {Key: "secondaryCatchUpPeriodSecs", Value: 0}, {Key: "force", Value: true},
		}).Err()
		err := w.tick(ctx)
		require.Error(t, err)
		require.True(t, isTransient(err), "%v", err)
		require.Equal(t, 3, w.events.Len(), "the batch is queued again")

		waitForPrimary(t, client)
		require.NoError(t, w.tick(ctx))
		require.Equal(t, int64(3), count(env, schema.CollectionRooms, bson.D{}))
		require.Equal(t, int64(3), count(env, schema.CollectionEvents, bson.D{}))
		require.Equal(t, float64(1), counterValue(w.metrics.batches, "retry")-retriesBefore)
	})
}

func TestStopWithUnreachableDatabase(t *testing.T) {
	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://127.0.0.1:1/?directConnection=true&serverSelectionTimeoutMS=60000"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	w := newWriter(store.New(client.Database("unreachable")), testSettings(t, nil), Node{ID: testNode}, newMetrics())
	droppedBefore := counterValue(w.metrics.dropped, "events", "shutdown")
	go w.run()
	w.enqueueEvents(roomEvents(10))

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	w.stop(ctx)
	require.Less(t, time.Since(started), ShutdownTimeout+500*time.Millisecond)

	select {
	case <-w.stopped:
	case <-time.After(time.Second):
		t.Fatal("the batch loop is still running after the shutdown deadline")
	}
	require.Equal(t, float64(10), counterValue(w.metrics.dropped, "events", "shutdown")-droppedBefore)
}

// legacyDatabase returns an empty database whose events collection has the shape of the previous writer.
func legacyDatabase(t *testing.T, client *mongo.Client, name string) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	db := client.Database(name)
	require.NoError(t, db.Drop(ctx))
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	_, err := db.Collection(schema.CollectionEvents).InsertMany(ctx, []any{
		bson.D{{Key: "type", Value: "roomCreated"}, {Key: "room", Value: "room"}, {Key: "timestamp", Value: int64(1760263200000)},
			{Key: "openvidu_expire_at", Value: time.UnixMilli(1762855200000)}},
		bson.D{{Key: "type", Value: "roomDestroyed"}, {Key: "room", Value: "room"}, {Key: "timestamp", Value: int64(1760263260000)},
			{Key: "openvidu_expire_at", Value: time.UnixMilli(1762855260000)}},
	})
	require.NoError(t, err)
	return db
}

func countIn(t *testing.T, db *mongo.Database, collection string) int64 {
	t.Helper()
	n, err := db.Collection(collection).CountDocuments(context.Background(), bson.D{})
	require.NoError(t, err)
	return n
}

func hasValidator(t *testing.T, db *mongo.Database, collection string) bool {
	t.Helper()
	cursor, err := db.ListCollections(context.Background(), bson.D{{Key: "name", Value: collection}})
	require.NoError(t, err)
	var infos []struct {
		Options struct {
			Validator bson.Raw `bson:"validator"`
		} `bson:"options"`
	}
	require.NoError(t, cursor.All(context.Background(), &infos))
	require.Len(t, infos, 1)
	return len(infos[0].Options.Validator) > 0
}

func waitForPrimary(t *testing.T, client *mongo.Client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var hello struct {
			IsWritablePrimary bool `bson:"isWritablePrimary"`
		}
		err := client.Database("admin").RunCommand(context.Background(), bson.D{{Key: "hello", Value: 1}}).Decode(&hello)
		if err == nil && hello.IsWritablePrimary {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no primary after the step-down")
}
