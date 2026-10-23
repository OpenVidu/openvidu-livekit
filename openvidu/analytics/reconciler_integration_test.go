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
	"fmt"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	mongotest "github.com/livekit/livekit-server/openvidu/analytics/testutil"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
)

const (
	nodeAlive = "ND_alive"
	nodeDead  = "ND_dead"
)

// reconcilerEnv is a writer environment with a reconciler over the same database, a fake live state and a clock.
type reconcilerEnv struct {
	*testEnv
	live    *fakeLive
	r       *reconciler
	logs    *capturedLog
	metrics *reconcilerMetrics
	now     time.Time
	ev      events
}

func newReconcilerEnv(t *testing.T, client *mongo.Client, configure ...func(*openviduconfig.AnalyticsConfig)) *reconcilerEnv {
	t.Helper()
	env := &reconcilerEnv{testEnv: newEnv(t, client, configure...), live: newFakeLive(), logs: &capturedLog{},
		metrics: buildReconcilerMetrics(), now: reconcileNow}
	env.r = newReconciler(store.New(env.db), env.live, projectionOptions(env.settings), reconcilerSettings{
		batchInterval: env.settings.Interval, interval: env.settings.Reconciler.Interval, cleanupInterval: 3 * time.Minute,
		maxClosures: env.settings.Reconciler.MaxClosuresPerRun, entitiesRetention: env.settings.Retention.Entities,
	}, "ND_leader", env.metrics)
	env.r.log = env.logs.sink()
	env.r.now = func() time.Time { return env.now }
	env.heartbeat(nodeAlive, reconcileNow.Add(-5*time.Second))
	return env
}

func (e *reconcilerEnv) heartbeat(node string, at time.Time) {
	e.t.Helper()
	require.NoError(e.t, store.New(e.db).Heartbeat(context.Background(), node, "test", "", 10*time.Second, at))
}

func (e *reconcilerEnv) reconcile() runReport {
	e.t.Helper()
	report := e.r.reconcile(context.Background(), false)
	require.NoError(e.t, report.err)
	return report
}

func before(d time.Duration) time.Time {
	return reconcileNow.Add(-d)
}

// on moves an event to a node, as the node hosting the room emits it.
func on(node string, event *livekit.AnalyticsEvent) *livekit.AnalyticsEvent {
	event.NodeId = node
	if event.ClientMeta != nil {
		event.ClientMeta.Node = node
	}
	return event
}

// room creates a room on node at createdAt.
func (e *reconcilerEnv) room(sid string, node string, createdAt time.Time) {
	e.apply([]*livekit.AnalyticsEvent{on(node, e.ev.roomCreated(sid, "room-"+sid, createdAt))})
}

// session joins a participant at joinedAt, active a second later unless joining is set.
func (e *reconcilerEnv) session(room string, node string, sid string, kind livekit.ParticipantInfo_Kind, joinedAt time.Time,
	joining bool) *livekit.ParticipantInfo {
	info := participantInfo(sid, "id-"+sid, kind, joinedAt)
	batch := []*livekit.AnalyticsEvent{on(node, e.ev.joined(room, info, joinedAt))}
	if !joining {
		batch = append(batch, on(node, e.ev.active(room, info, joinedAt.Add(time.Second))))
	}
	e.apply(batch)
	return info
}

// statsAt flushes the stats of a participant at when.
func (e *reconcilerEnv) statsAt(room string, node string, sid string, when time.Time) {
	s := stat(room, sid, "", livekit.StreamType_UPSTREAM, when, 4.2, 1000)
	s.Node = node
	e.apply(nil, []*livekit.AnalyticsStat{s})
}

func TestReconcilerCandidates(t *testing.T) {
	client := mongotest.StartMongo(t)
	ctx := context.Background()

	t.Run("participant thresholds", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		env.room("RM_p", nodeAlive, before(time.Hour))
		env.session("RM_p", nodeAlive, "PA_stats_stopped", livekit.ParticipantInfo_STANDARD, before(30*time.Minute), false)
		env.statsAt("RM_p", nodeAlive, "PA_stats_stopped", before(100*time.Second))
		env.session("RM_p", nodeAlive, "PA_at_threshold", livekit.ParticipantInfo_STANDARD, before(30*time.Minute), false)
		env.statsAt("RM_p", nodeAlive, "PA_at_threshold", before(staleAfter))
		env.session("RM_p", nodeAlive, "PA_never_stats", livekit.ParticipantInfo_STANDARD, before(96*time.Second), false)
		env.session("RM_p", nodeAlive, "PA_grace", livekit.ParticipantInfo_STANDARD, before(41*time.Second), false)
		env.session("RM_p", nodeAlive, "PA_joining", livekit.ParticipantInfo_STANDARD, before(95*time.Second), true)
		left := env.session("RM_p", nodeAlive, "PA_left", livekit.ParticipantInfo_STANDARD, before(30*time.Minute), false)
		env.apply([]*livekit.AnalyticsEvent{on(nodeAlive, env.ev.left("RM_p", left, before(20*time.Minute),
			livekit.DisconnectReason_CLIENT_INITIATED))})

		found, err := env.r.candidates(ctx, reconcileNow)
		require.NoError(t, err)
		var sids []string
		for _, participant := range found.participants {
			sids = append(sids, participant.SID)
		}
		require.ElementsMatch(t, []string{"PA_stats_stopped", "PA_never_stats", "PA_joining"}, sids)
		require.Equal(t, "PA_stats_stopped", sids[0], "sessions with stats come first, the stalest first")
	})

	t.Run("room thresholds", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		// Empty timeout 300 s plus two intervals of 60 s: a room is a candidate 7 minutes after its last event.
		env.room("RM_empty", nodeAlive, before(7*time.Minute+time.Second))
		env.room("RM_recent", nodeAlive, before(7*time.Minute-time.Second))
		env.room("RM_kept", nodeAlive, before(30*time.Minute))
		env.session("RM_kept", nodeAlive, "PA_kept", livekit.ParticipantInfo_STANDARD, before(20*time.Minute), false)
		env.room("RM_dependent", nodeAlive, before(30*time.Minute))
		env.session("RM_dependent", nodeAlive, "PA_recorder", livekit.ParticipantInfo_EGRESS, before(20*time.Minute), false)
		env.room("RM_streaming", nodeAlive, before(30*time.Minute))
		env.session("RM_streaming", nodeAlive, "PA_streamer", livekit.ParticipantInfo_EGRESS, before(20*time.Minute), false)
		env.statsAt("RM_streaming", nodeAlive, "PA_streamer", before(time.Minute))
		// A dead node's room does not wait for its empty timeout, nor for its open sessions to close.
		env.room("RM_dead_recent", nodeDead, before(2*time.Minute))
		env.session("RM_dead_recent", nodeDead, "PA_crashed", livekit.ParticipantInfo_STANDARD, before(2*time.Minute), false)
		env.statsAt("RM_dead_recent", nodeDead, "PA_crashed", before(100*time.Second))
		env.room("RM_alive_waiting", nodeAlive, before(2*time.Minute))

		found, err := env.r.candidates(ctx, reconcileNow)
		require.NoError(t, err)
		sidsOf := func(rooms []store.OpenRoom) []string {
			var sids []string
			for _, room := range rooms {
				sids = append(sids, room.SID)
			}
			return sids
		}
		require.ElementsMatch(t, []string{"RM_empty", "RM_dependent"}, sidsOf(found.rooms))
		require.Equal(t, []string{"RM_dead_recent"}, sidsOf(found.deadRooms))
		require.EqualValues(t, 3, found.counts().Room)
	})

	t.Run("egress and ingress session thresholds", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		ended := func(room string, node string, at time.Time) {
			env.room(room, node, before(time.Hour))
			env.apply([]*livekit.AnalyticsEvent{on(node, env.ev.roomEnded(room, "room-"+room, before(time.Hour), at,
				livekit.RoomEndReason_ROOM_END_API_DELETE))})
		}
		ended("RM_dead_closed", nodeDead, before(7*time.Minute))
		ended("RM_alive_closed", nodeAlive, before(10*time.Minute))
		ended("RM_dead_recent", nodeDead, before(5*time.Minute))
		env.room("RM_active", nodeDead, before(time.Hour))
		for _, room := range []string{"RM_dead_closed", "RM_alive_closed", "RM_dead_recent", "RM_active"} {
			egress := roomCompositeEgress("EG_"+room, room, livekit.EgressStatus_EGRESS_ACTIVE, before(50*time.Minute),
				before(40*time.Minute))
			ingress := rtmpIngress("IN_"+room, "room-"+room, ingressState(livekit.IngressState_ENDPOINT_PUBLISHING, room,
				"rtmp_"+room, before(50*time.Minute), time.Time{}))
			env.apply([]*livekit.AnalyticsEvent{
				env.ev.egress(livekit.AnalyticsEventType_EGRESS_STARTED, egress, before(50*time.Minute)),
				env.ev.ingress(livekit.AnalyticsEventType_INGRESS_STARTED, ingress, before(50*time.Minute)),
			})
		}

		found, err := env.r.candidates(ctx, reconcileNow)
		require.NoError(t, err)
		require.Len(t, found.egresses, 1)
		require.Equal(t, "EG_RM_dead_closed", found.egresses[0].egress.ID)
		require.Len(t, found.ingressSessions, 1)
		require.Equal(t, "rtmp_RM_dead_closed", found.ingressSessions[0].session.ID)
	})
}

func TestReconcilerQueryPlans(t *testing.T) {
	client := mongotest.StartMongo(t)
	ctx := context.Background()
	env := newReconcilerEnv(t, client)
	const limit = 50

	// Scenario S of the dashboard data generator in miniature: mostly closed history, a few hundred open sessions with fresh stats
	// and a handful of stale ones.
	for r := range 40 {
		room := fmt.Sprintf("RM_%03d", r)
		env.room(room, nodeAlive, before(3*time.Hour))
		var batch []*livekit.AnalyticsEvent
		for p := range 60 {
			info := participantInfo(fmt.Sprintf("PA_%03d_%02d", r, p), fmt.Sprintf("user-%d", p),
				livekit.ParticipantInfo_STANDARD, before(2*time.Hour))
			batch = append(batch, on(nodeAlive, env.ev.joined(room, info, before(2*time.Hour))),
				on(nodeAlive, env.ev.active(room, info, before(2*time.Hour-time.Second))))
			if p >= 8 {
				batch = append(batch, on(nodeAlive, env.ev.left(room, info, before(time.Hour),
					livekit.DisconnectReason_CLIENT_INITIATED)))
			}
		}
		if r >= 30 {
			batch = append(batch, on(nodeAlive, env.ev.roomEnded(room, "room-"+room, before(3*time.Hour), before(50*time.Minute),
				livekit.RoomEndReason_ROOM_END_API_DELETE)))
		}
		env.apply(batch)
		var stats []*livekit.AnalyticsStat
		for p := range 8 {
			when := before(10 * time.Second)
			if p == 0 {
				when = before(5 * time.Minute)
			}
			s := stat(room, fmt.Sprintf("PA_%03d_%02d", r, p), "", livekit.StreamType_UPSTREAM, when, 4.2, 1000)
			s.Node = nodeAlive
			stats = append(stats, s)
		}
		env.apply(nil, stats)
	}

	staleBefore := reconcileNow.Add(-staleAfter)
	for _, query := range append(store.StaleParticipantQueries(staleBefore, limit),
		store.StaleRoomQuery(reconcileNow, 2*time.Minute, staleBefore, limit), store.IdleRoomQuery(staleBefore, limit)) {
		stats := explainFind(t, env.db, query)
		require.NotContains(t, stats.stages, "COLLSCAN", query.Collection)
		require.Contains(t, stats.stages, "IXSCAN", query.Collection)
		require.LessOrEqual(t, stats.docsExamined, int64(limit), "%s %v", query.Collection, query.Filter)
	}
	require.Contains(t, explainFind(t, env.db, store.IdleRoomQuery(staleBefore, limit)).indexes, "status_lastEventAt_active")

	distinct := explain(t, env.db, bson.D{{Key: "distinct", Value: schema.CollectionParticipants}, {Key: "key", Value: "roomSid"},
		{Key: "query", Value: store.OpenSessionsFilter([]string{"RM_001", "RM_002"}, projection.DependentKinds)}})
	require.Contains(t, distinct.stages, "IXSCAN")
	require.NotContains(t, distinct.stages, "COLLSCAN")

	aggregate := explain(t, env.db, bson.D{{Key: "aggregate", Value: schema.CollectionParticipants},
		{Key: "pipeline", Value: store.UniqueIdentitiesPipeline(before(24*time.Hour), reconcileNow)},
		{Key: "cursor", Value: bson.D{}}})
	require.Contains(t, aggregate.indexes, "kind_status_joinedAt_id")
	require.NotContains(t, aggregate.stages, "COLLSCAN")

	found, err := env.r.candidates(ctx, reconcileNow)
	require.NoError(t, err)
	require.Len(t, found.participants, 40, "one stale session per room")
}

type planStats struct {
	stages       []string
	indexes      []string
	docsExamined int64
}

func explainFind(t *testing.T, db *mongo.Database, query store.Query) planStats {
	command := bson.D{{Key: "find", Value: query.Collection}, {Key: "filter", Value: query.Filter}}
	if query.Sort != nil {
		command = append(command, bson.E{Key: "sort", Value: query.Sort})
	}
	if query.Limit > 0 {
		command = append(command, bson.E{Key: "limit", Value: query.Limit})
	}
	return explain(t, db, command)
}

func explain(t *testing.T, db *mongo.Database, command bson.D) planStats {
	t.Helper()
	var result bson.M
	require.NoError(t, db.RunCommand(context.Background(), bson.D{{Key: "explain", Value: command},
		{Key: "verbosity", Value: "executionStats"}}).Decode(&result))
	var stats planStats
	var walk func(value any)
	walk = func(value any) {
		switch v := value.(type) {
		case bson.M:
			if stage, ok := v["stage"].(string); ok {
				stats.stages = append(stats.stages, stage)
			}
			if index, ok := v["indexName"].(string); ok {
				stats.indexes = append(stats.indexes, index)
			}
			if examined, ok := v["totalDocsExamined"]; ok {
				if n, ok := examined.(int32); ok {
					stats.docsExamined = max(stats.docsExamined, int64(n))
				}
				if n, ok := examined.(int64); ok {
					stats.docsExamined = max(stats.docsExamined, n)
				}
			}
			for key, item := range v {
				if key != "rejectedPlans" {
					walk(item)
				}
			}
		case bson.A:
			for _, item := range v {
				walk(item)
			}
		case bson.D:
			m := bson.M{}
			for _, element := range v {
				m[element.Key] = element.Value
			}
			walk(m)
		}
	}
	walk(result)
	return stats
}

func TestReconcilerClosures(t *testing.T) {
	client := mongotest.StartMongo(t)
	ctx := context.Background()

	// publisherWithSubscribers builds a room on the dead node where PA_pub publishes two tracks subscribed by three
	// participants whose stats are fresh, so only PA_pub is a candidate.
	publisherWithSubscribers := func(env *reconcilerEnv) {
		env.room("RM_c", nodeDead, before(time.Hour))
		publisher := env.session("RM_c", nodeDead, "PA_pub", livekit.ParticipantInfo_STANDARD, before(50*time.Minute), false)
		camera, mic := cameraTrack("TR_cam"), microphoneTrack("TR_mic")
		batch := []*livekit.AnalyticsEvent{
			on(nodeDead, env.ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_c", publisher, camera, before(49*time.Minute))),
			on(nodeDead, env.ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_c", publisher, mic, before(49*time.Minute))),
		}
		env.apply(batch)
		for i := range 3 {
			sid := fmt.Sprintf("PA_sub%d", i)
			env.session("RM_c", nodeDead, sid, livekit.ParticipantInfo_STANDARD, before(48*time.Minute), false)
			env.apply([]*livekit.AnalyticsEvent{
				on(nodeDead, env.ev.subscription(livekit.AnalyticsEventType_TRACK_SUBSCRIBED, "RM_c", sid, camera, publisher, before(47*time.Minute))),
				on(nodeDead, env.ev.subscription(livekit.AnalyticsEventType_TRACK_SUBSCRIBED, "RM_c", sid, mic, publisher, before(47*time.Minute))),
			})
			env.statsAt("RM_c", nodeDead, sid, before(5*time.Second))
		}
		env.statsAt("RM_c", nodeDead, "PA_pub", before(100*time.Second))
	}

	t.Run("participant cascade, counters, rollups, event, log and run record", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		publisherWithSubscribers(env)

		report := env.reconcile()
		require.Equal(t, resultOK, report.result)
		require.Equal(t, schema.ReconcilerClosures{Participant: 1, Track: 2, Subscription: 6}, report.closed)

		leftAt := before(100 * time.Second)
		participant := find[schema.ParticipantDocument](env.testEnv, schema.CollectionParticipants, "PA_pub")
		require.Equal(t, "left", participant.Status)
		require.True(t, leftAt.Equal(*participant.LeftAt))
		require.Equal(t, "RECONCILED", participant.DisconnectReason)
		require.Equal(t, "reconciler", participant.ClosedBy)
		require.Equal(t, leftAt.Sub(participant.JoinedAt).Milliseconds(), *participant.DurationMs)
		require.True(t, leftAt.Add(2160*time.Hour).Equal(*participant.ExpiresAt))

		for _, track := range []string{"TR_cam", "TR_mic"} {
			document := find[schema.TrackDocument](env.testEnv, schema.CollectionTracks, track)
			require.Equal(t, "unpublished", document.Status)
			require.True(t, leftAt.Equal(*document.UnpublishedAt))
			require.Equal(t, "reconciler", document.ClosedBy)
		}
		require.EqualValues(t, 6, count(env.testEnv, schema.CollectionTrackSubscriptions, bson.D{
			{Key: "status", Value: "unsubscribed"}, {Key: "unsubscribedAt", Value: leftAt}, {Key: "closedBy", Value: "reconciler"}}))

		room := find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_c")
		require.EqualValues(t, 3, room.Participants.Active)
		require.Equal(t, "active", room.Status)

		hourly := find[schema.HourlyRollupDocument](env.testEnv, schema.CollectionAnalyticsHourly,
			"global:"+leftAt.Truncate(time.Hour).Format(time.RFC3339))
		require.EqualValues(t, 1, *hourly.Participants.Left)
		require.EqualValues(t, 1, hourly.Participants.DisconnectReasons["RECONCILED"])
		find[schema.HourlyRollupDocument](env.testEnv, schema.CollectionAnalyticsHourly,
			"node:"+nodeDead+":"+leftAt.Truncate(time.Hour).Format(time.RFC3339))

		event := find[syntheticEvent](env.testEnv, schema.CollectionEvents, "AE_SYN_PA_pub")
		require.Equal(t, "PARTICIPANT_LEFT", event.Type)
		require.True(t, event.Synthetic)
		require.Equal(t, "RM_c", event.RoomSID)
		require.Equal(t, "PA_pub", event.ParticipantSID)
		require.Equal(t, nodeDead, event.NodeID)
		require.True(t, leftAt.Equal(event.TS))
		require.Equal(t, "RECONCILED", event.Payload.Participant.DisconnectReason)
		require.Equal(t, reasonNodeDead, event.Payload.Reconciler.Reason)

		lines := env.logs.matching("info", "analytics reconciler: closed")
		require.Len(t, lines, 1)
		for _, part := range []string{"PA_pub", "RM_c", nodeDead, "reason" + reasonNodeDead, "leftAt" + leftAt.String()} {
			require.Contains(t, lines[0], part)
		}

		run := find[schema.SchemaMetaReconciler](env.testEnv, schema.CollectionSchemaMeta, "reconciler")
		require.True(t, reconcileNow.Equal(run.LastRunAt))
		require.Equal(t, "ND_leader", run.NodeID)
		require.Equal(t, schema.ReconcilerCounts{Participant: 1}, run.Candidates)
		require.Equal(t, report.closed, run.Closed)
		require.False(t, run.Partial)
		require.Empty(t, run.LastError)
	})

	t.Run("a service closure counts no participant", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		env.room("RM_s", nodeDead, before(time.Hour))
		env.session("RM_s", nodeDead, "PA_alice", livekit.ParticipantInfo_STANDARD, before(30*time.Minute), false)
		env.session("RM_s", nodeDead, "PA_cam", livekit.ParticipantInfo_INGRESS, before(30*time.Minute), false)
		env.session("RM_s", nodeDead, "PA_sip", livekit.ParticipantInfo_SIP, before(30*time.Minute), true)

		report := env.reconcile()
		require.EqualValues(t, 3, report.closed.Participant)
		cursor, err := env.db.Collection(schema.CollectionAnalyticsHourly).Find(ctx,
			bson.D{{Key: "_id", Value: bson.D{{Key: "$regex", Value: "^global:"}}}})
		require.NoError(t, err)
		var hourly []schema.HourlyRollupDocument
		require.NoError(t, cursor.All(ctx, &hourly))
		var left, aborted, reconciled int32
		for _, document := range hourly {
			if document.Participants == nil {
				continue
			}
			left += valueOf(document.Participants.Left)
			aborted += valueOf(document.Participants.Aborted)
			reconciled += document.Participants.DisconnectReasons["RECONCILED"]
		}
		require.Equal(t, []int32{1, 0, 1}, []int32{left, aborted, reconciled})
		room := find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_s")
		require.EqualValues(t, 0, room.Participants.Active)
		require.EqualValues(t, 0, room.Participants.Aborted)
	})

	t.Run("metrics of a closure", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		env.room("RM_m", nodeDead, before(time.Hour))
		publisher := env.session("RM_m", nodeDead, "PA_pub", livekit.ParticipantInfo_STANDARD, before(50*time.Minute), false)
		camera := cameraTrack("TR_cam")
		env.apply([]*livekit.AnalyticsEvent{on(nodeDead,
			env.ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_m", publisher, camera, before(49*time.Minute)))})
		for i := range 2 {
			sid := fmt.Sprintf("PA_sub%d", i)
			env.session("RM_m", nodeDead, sid, livekit.ParticipantInfo_STANDARD, before(48*time.Minute), false)
			env.apply([]*livekit.AnalyticsEvent{on(nodeDead, env.ev.subscription(livekit.AnalyticsEventType_TRACK_SUBSCRIBED,
				"RM_m", sid, camera, publisher, before(47*time.Minute)))})
			env.statsAt("RM_m", nodeDead, sid, before(5*time.Second))
		}
		env.statsAt("RM_m", nodeDead, "PA_pub", before(100*time.Second))

		env.reconcile()
		closed := func(entity string) float64 { return counterValue(env.metrics.closed, entity) }
		require.Equal(t, []float64{1, 1, 2}, []float64{closed(entityParticipant), closed(entityTrack), closed(entitySubscription)})
		require.Equal(t, float64(1), counterValue(env.metrics.runs, resultOK))
		require.Equal(t, float64(1), testutil.ToFloat64(env.metrics.candidates.WithLabelValues(entityParticipant)))
	})

	t.Run("a repeated closure changes nothing", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		publisherWithSubscribers(env)
		env.reconcile()
		first := snapshot(env.testEnv, allCollections...)

		report := env.reconcile()
		require.Equal(t, schema.ReconcilerClosures{}, report.closed)
		require.Equal(t, first, snapshot(env.testEnv, allCollections...))
		require.EqualValues(t, 1, count(env.testEnv, schema.CollectionEvents, bson.D{{Key: "synthetic", Value: true}}))
	})

	t.Run("a stats flush between confirmation and closure vetoes it", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		publisherWithSubscribers(env)
		env.r.store = &flushingStore{Store: store.New(env.db), db: env.db, at: reconcileNow.Add(-time.Second)}

		report := env.reconcile()
		require.Equal(t, schema.ReconcilerClosures{}, report.closed)
		participant := find[schema.ParticipantDocument](env.testEnv, schema.CollectionParticipants, "PA_pub")
		require.Equal(t, "active", participant.Status)
		require.EqualValues(t, 2, count(env.testEnv, schema.CollectionTracks, bson.D{{Key: "status", Value: "published"}}))
		require.Len(t, env.logs.matching("debug", "closure skipped"), 1)
	})

	t.Run("a late close event corrects the closure without counting it again", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		publisherWithSubscribers(env)
		env.reconcile()

		pub := participantInfo("PA_pub", "id-PA_pub", livekit.ParticipantInfo_STANDARD, before(50*time.Minute))
		realLeft := before(80 * time.Second)
		env.apply([]*livekit.AnalyticsEvent{on(nodeDead, env.ev.left("RM_c", pub, realLeft, livekit.DisconnectReason_CLIENT_INITIATED))})

		participant := find[schema.ParticipantDocument](env.testEnv, schema.CollectionParticipants, "PA_pub")
		require.Equal(t, "left", participant.Status)
		require.True(t, realLeft.Equal(*participant.LeftAt))
		require.Equal(t, "CLIENT_INITIATED", participant.DisconnectReason)
		require.Equal(t, "event", participant.ClosedBy)

		hourly := find[schema.HourlyRollupDocument](env.testEnv, schema.CollectionAnalyticsHourly,
			"global:"+realLeft.Truncate(time.Hour).Format(time.RFC3339))
		require.EqualValues(t, 1, *hourly.Participants.Left)
		require.EqualValues(t, 1, hourly.Participants.DisconnectReasons["RECONCILED"])
		require.NotContains(t, hourly.Participants.DisconnectReasons, "CLIENT_INITIATED")
	})

	t.Run("a session that never became active aborts", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		env.room("RM_j", nodeDead, before(time.Hour))
		env.session("RM_j", nodeDead, "PA_joining", livekit.ParticipantInfo_STANDARD, before(95*time.Second), true)

		report := env.reconcile()
		require.Equal(t, int32(1), report.closed.Participant)
		participant := find[schema.ParticipantDocument](env.testEnv, schema.CollectionParticipants, "PA_joining")
		require.Equal(t, "aborted", participant.Status)
		require.Equal(t, "RECONCILED", participant.DisconnectReason)
		room := find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_j")
		require.EqualValues(t, 1, room.Participants.Aborted)
		require.Equal(t, "PARTICIPANT_CONNECTION_ABORTED",
			find[syntheticEvent](env.testEnv, schema.CollectionEvents, "AE_SYN_PA_joining").Type)
		hourly := find[schema.HourlyRollupDocument](env.testEnv, schema.CollectionAnalyticsHourly,
			"global:"+before(95*time.Second).Truncate(time.Hour).Format(time.RFC3339))
		require.EqualValues(t, 1, *hourly.Participants.Aborted)
	})

	t.Run("a room closes with its open sessions", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		created := before(time.Hour)
		env.room("RM_r", nodeDead, created)
		env.session("RM_r", nodeDead, "PA_recorder", livekit.ParticipantInfo_EGRESS, before(50*time.Minute), false)
		env.statsAt("RM_r", nodeDead, "PA_recorder", before(time.Minute))
		room := find[store.OpenRoom](env.testEnv, schema.CollectionRooms, "RM_r")

		report := &runReport{result: resultOK}
		_, err := env.r.close(ctx, confirmation{rooms: []confirmedRoom{{OpenRoom: room, reason: reasonNodeDead}}},
			reconcileNow.Add(time.Minute), report)
		require.NoError(t, err)
		require.Equal(t, schema.ReconcilerClosures{Room: 1, Participant: 1}, report.closed)

		closed := find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_r")
		require.Equal(t, "closed", closed.Status)
		require.Equal(t, "RECONCILED", closed.EndReason)
		require.Equal(t, "reconciler", closed.ClosedBy)
		require.True(t, before(time.Minute).Equal(*closed.EndedAt), "end time is the room's last stats flush")
		require.EqualValues(t, 0, closed.Participants.Active)
		participant := find[schema.ParticipantDocument](env.testEnv, schema.CollectionParticipants, "PA_recorder")
		require.Equal(t, "left", participant.Status)
		require.True(t, closed.EndedAt.Equal(*participant.LeftAt))
		require.Equal(t, "RECONCILED", participant.DisconnectReason)
		require.Len(t, env.logs.matching("info", "reason"+reasonCascade), 1)

		hourly := find[schema.HourlyRollupDocument](env.testEnv, schema.CollectionAnalyticsHourly,
			"global:"+closed.EndedAt.Truncate(time.Hour).Format(time.RFC3339))
		require.EqualValues(t, 1, *hourly.Rooms.Ended)
		require.EqualValues(t, 1, hourly.Rooms.EndedByReason["RECONCILED"])
		require.EqualValues(t, 0, *hourly.Rooms.ParticipantsSum, "dependent sessions do not count")
		require.Equal(t, closed.EndedAt.Sub(created).Milliseconds(), *hourly.Rooms.DurationMsSum)
		require.EqualValues(t, 1, count(env.testEnv, schema.CollectionEvents, bson.D{{Key: "_id", Value: "AE_SYN_RM_r"},
			{Key: "type", Value: "ROOM_ENDED"}, {Key: "payload.room_end_reason", Value: "RECONCILED"}}))

		realEnd := before(30 * time.Second)
		env.apply([]*livekit.AnalyticsEvent{on(nodeDead, env.ev.roomEnded("RM_r", "room-RM_r", created, realEnd,
			livekit.RoomEndReason_ROOM_END_IDLE_TIMEOUT))})
		closed = find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_r")
		require.True(t, realEnd.Equal(*closed.EndedAt))
		require.Equal(t, "IDLE_TIMEOUT", closed.EndReason)
		require.Equal(t, "event", closed.ClosedBy)
		hourly = find[schema.HourlyRollupDocument](env.testEnv, schema.CollectionAnalyticsHourly,
			"global:"+before(time.Minute).Truncate(time.Hour).Format(time.RFC3339))
		require.EqualValues(t, 1, *hourly.Rooms.Ended)
		require.NotContains(t, hourly.Rooms.EndedByReason, "IDLE_TIMEOUT")
	})

	t.Run("a room of a dead node closes with its last session", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		created := before(2 * time.Minute)
		env.room("RM_d", nodeDead, created)
		env.session("RM_d", nodeDead, "PA_a", livekit.ParticipantInfo_STANDARD, created, false)
		env.session("RM_d", nodeDead, "PA_b", livekit.ParticipantInfo_STANDARD, created, false)
		env.statsAt("RM_d", nodeDead, "PA_a", before(110*time.Second))
		env.statsAt("RM_d", nodeDead, "PA_b", before(100*time.Second))

		report := env.reconcile()
		require.Equal(t, schema.ReconcilerClosures{Room: 1, Participant: 2}, report.closed)
		closed := find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_d")
		require.Equal(t, "closed", closed.Status)
		require.Equal(t, "RECONCILED", closed.EndReason)
		require.Equal(t, "reconciler", closed.ClosedBy)
		last := find[schema.ParticipantDocument](env.testEnv, schema.CollectionParticipants, "PA_b")
		require.True(t, before(100*time.Second).Equal(*last.LeftAt))
		require.True(t, last.LeftAt.Equal(*closed.EndedAt), "the room ends with its last session")
		require.EqualValues(t, 1, count(env.testEnv, schema.CollectionEvents, bson.D{{Key: "_id", Value: "AE_SYN_RM_d"},
			{Key: "type", Value: "ROOM_ENDED"}, {Key: "payload.reconciler.reason", Value: reasonNodeDead}}))
		hourly := find[schema.HourlyRollupDocument](env.testEnv, schema.CollectionAnalyticsHourly,
			"global:"+closed.EndedAt.Truncate(time.Hour).Format(time.RFC3339))
		require.EqualValues(t, 1, *hourly.Rooms.Ended)
	})

	t.Run("a room still listed waits for the dead node cleanup", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		env.room("RM_w", nodeDead, before(2*time.Minute))
		env.session("RM_w", nodeDead, "PA_w", livekit.ParticipantInfo_STANDARD, before(2*time.Minute), false)
		env.statsAt("RM_w", nodeDead, "PA_w", before(100*time.Second))
		env.live.addRoom("room-RM_w", "RM_w")

		require.Equal(t, schema.ReconcilerClosures{Participant: 1}, env.reconcile().closed)
		require.Equal(t, "active", find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_w").Status)
		require.Len(t, env.logs.matching("debug", "room of a dead node kept, LiveKit still lists it"), 1)

		delete(env.live.rooms, "room-RM_w")
		env.now = reconcileNow.Add(time.Minute)
		env.heartbeat(nodeAlive, env.now.Add(-time.Second))
		require.Equal(t, schema.ReconcilerClosures{Room: 1}, env.reconcile().closed)
		closed := find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_w")
		participant := find[schema.ParticipantDocument](env.testEnv, schema.CollectionParticipants, "PA_w")
		require.Equal(t, "closed", closed.Status)
		require.True(t, participant.LeftAt.Equal(*closed.EndedAt), "the end is its last session's, not the run's")
	})

	t.Run("the closure limit keeps the room of a dead node open", func(t *testing.T) {
		env := newReconcilerEnv(t, client, func(c *openviduconfig.AnalyticsConfig) { c.Reconciler.MaxClosuresPerRun = 2 })
		env.room("RM_l", nodeDead, before(2*time.Minute))
		for i := range 3 {
			sid := fmt.Sprintf("PA_l%d", i)
			env.session("RM_l", nodeDead, sid, livekit.ParticipantInfo_STANDARD, before(2*time.Minute), false)
			env.statsAt("RM_l", nodeDead, sid, before(100*time.Second))
		}

		first := env.r.reconcile(ctx, false)
		require.NoError(t, first.err)
		require.Equal(t, schema.ReconcilerClosures{Participant: 2}, first.closed)
		require.True(t, first.partial)
		require.Equal(t, "active", find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_l").Status)

		next := env.r.reconcile(ctx, true)
		require.NoError(t, next.err)
		require.Equal(t, schema.ReconcilerClosures{Room: 1, Participant: 1}, next.closed)
		require.Equal(t, "closed", find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_l").Status)
	})

	t.Run("egress and ingress sessions lost with their node", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		env.room("RM_e", nodeDead, before(time.Hour))
		egress := roomCompositeEgress("EG_lost", "RM_e", livekit.EgressStatus_EGRESS_ACTIVE, before(50*time.Minute),
			before(40*time.Minute))
		ingress := rtmpIngress("IN_lost", "room-RM_e", ingressState(livekit.IngressState_ENDPOINT_PUBLISHING, "RM_e",
			"rtmp_lost", before(45*time.Minute), time.Time{}))
		roomEnd := before(10 * time.Minute)
		env.apply([]*livekit.AnalyticsEvent{
			env.ev.egress(livekit.AnalyticsEventType_EGRESS_STARTED, egress, before(50*time.Minute)),
			env.ev.ingress(livekit.AnalyticsEventType_INGRESS_STARTED, ingress, before(45*time.Minute)),
			on(nodeDead, env.ev.roomEnded("RM_e", "room-RM_e", before(time.Hour), roomEnd, livekit.RoomEndReason_ROOM_END_API_DELETE)),
		})

		report := env.reconcile()
		require.Equal(t, schema.ReconcilerClosures{Egress: 1, IngressSession: 1}, report.closed)

		lost := find[schema.EgressDocument](env.testEnv, schema.CollectionEgresses, "EG_lost")
		require.Equal(t, "failed", lost.Status)
		require.Equal(t, "egress lost (reconciled)", lost.Error)
		require.Equal(t, "reconciler", lost.ClosedBy)
		require.True(t, roomEnd.Equal(*lost.EndedAt), "end time is the later of the room end and the last egress update")
		require.EqualValues(t, 0, find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_e").Egress.Active)
		require.EqualValues(t, 1, count(env.testEnv, schema.CollectionEvents, bson.D{{Key: "_id", Value: "AE_SYN_EG_lost"},
			{Key: "type", Value: "EGRESS_ENDED"}, {Key: "synthetic", Value: true}}))

		session := find[schema.IngressSessionDocument](env.testEnv, schema.CollectionIngressSessions, "rtmp_lost")
		require.Equal(t, "error", session.Status)
		require.Equal(t, "reconciler", session.ClosedBy)
		require.True(t, roomEnd.Equal(*session.EndedAt))
		parent := find[schema.IngressDocument](env.testEnv, schema.CollectionIngresses, "IN_lost")
		require.Equal(t, "error", parent.Status)
		require.Equal(t, roomEnd.Sub(before(45*time.Minute)).Milliseconds(), parent.TotalDurationMs)
		require.EqualValues(t, 1, count(env.testEnv, schema.CollectionEvents, bson.D{{Key: "_id", Value: "AE_SYN_rtmp_lost"},
			{Key: "type", Value: "INGRESS_ENDED"}}))

		daily := find[schema.DailyRollupDocument](env.testEnv, schema.CollectionAnalyticsDaily,
			"global:"+roomEnd.Truncate(24*time.Hour).Format(time.RFC3339))
		require.EqualValues(t, 1, daily.Egress.EndedByStatus["failed"])
		require.EqualValues(t, 1, *daily.Ingress.SessionsEnded)
	})

	t.Run("a real egress lost event projects as failed", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		egress := roomCompositeEgress("EG_1", "RM_1", livekit.EgressStatus_EGRESS_ACTIVE, before(time.Hour), before(time.Hour))
		ended := roomCompositeEgress("EG_1", "RM_1", livekit.EgressStatus_EGRESS_FAILED, before(time.Hour), before(time.Minute))
		ended.Error = "egress lost"
		ended.EndedAt = before(time.Minute).UnixNano()
		env.apply([]*livekit.AnalyticsEvent{
			env.ev.egress(livekit.AnalyticsEventType_EGRESS_STARTED, egress, before(time.Hour)),
			env.ev.egress(livekit.AnalyticsEventType_EGRESS_ENDED, ended, before(time.Minute)),
		})
		document := find[schema.EgressDocument](env.testEnv, schema.CollectionEgresses, "EG_1")
		require.Equal(t, "failed", document.Status)
		require.Equal(t, "egress lost", document.Error)
		require.Equal(t, "event", document.ClosedBy)
	})

	t.Run("an alive node needs two absences", func(t *testing.T) {
		env := newReconcilerEnv(t, client)
		env.room("RM_a", nodeAlive, before(time.Hour))
		env.session("RM_a", nodeAlive, "PA_gone", livekit.ParticipantInfo_STANDARD, before(50*time.Minute), false)
		env.session("RM_a", nodeAlive, "PA_here", livekit.ParticipantInfo_STANDARD, before(50*time.Minute), false)
		env.statsAt("RM_a", nodeAlive, "PA_gone", before(2*time.Minute))
		env.statsAt("RM_a", nodeAlive, "PA_here", before(2*time.Minute))
		env.live.addRoom("room-RM_a", "RM_a", "PA_here")

		require.Equal(t, schema.ReconcilerClosures{}, env.reconcile().closed)
		env.now = reconcileNow.Add(time.Minute)
		env.heartbeat(nodeAlive, env.now.Add(-time.Second))
		require.Equal(t, int32(1), env.reconcile().closed.Participant)
		require.Equal(t, "left", find[schema.ParticipantDocument](env.testEnv, schema.CollectionParticipants, "PA_gone").Status)
		require.Equal(t, "active", find[schema.ParticipantDocument](env.testEnv, schema.CollectionParticipants, "PA_here").Status)
		require.Len(t, env.logs.matching("warn", "PA_here (lastStatsAt"), 2)
	})

	t.Run("a mass crash drains in partial runs", func(t *testing.T) {
		env := newReconcilerEnv(t, client, func(c *openviduconfig.AnalyticsConfig) { c.Reconciler.MaxClosuresPerRun = 5 })
		env.room("RM_x", nodeDead, before(time.Hour))
		for i := range 12 {
			sid := fmt.Sprintf("PA_%02d", i)
			env.session("RM_x", nodeDead, sid, livekit.ParticipantInfo_STANDARD, before(50*time.Minute), false)
			env.statsAt("RM_x", nodeDead, sid, before(2*time.Minute))
		}
		var closed []int32
		var partial []bool
		for _, continuation := range []bool{false, true, true} {
			report := env.r.reconcile(ctx, continuation)
			require.NoError(t, report.err)
			closed = append(closed, report.closed.Participant)
			partial = append(partial, report.partial)
		}
		require.Equal(t, []int32{5, 5, 2}, closed)
		require.Equal(t, []bool{true, true, false}, partial)
		require.True(t, find[schema.SchemaMetaReconciler](env.testEnv, schema.CollectionSchemaMeta, "reconciler").Closed.Participant == 2)
		require.EqualValues(t, 1, counterValue(env.metrics.runs, resultOK))
		require.EqualValues(t, 2, counterValue(env.metrics.runs, resultPartial))
	})
}

type syntheticEvent struct {
	Type           string    `bson:"type"`
	TS             time.Time `bson:"ts"`
	RoomSID        string    `bson:"roomSid"`
	ParticipantSID string    `bson:"participantSid"`
	NodeID         string    `bson:"nodeId"`
	Synthetic      bool      `bson:"synthetic"`
	Payload        struct {
		Participant struct {
			DisconnectReason string `bson:"disconnect_reason"`
		} `bson:"participant"`
		Reconciler struct {
			Reason string `bson:"reason"`
		} `bson:"reconciler"`
	} `bson:"payload"`
}

// flushingStore writes a stats flush on the entity right before the reconciler closes it.
type flushingStore struct {
	*store.Store
	db *mongo.Database
	at time.Time
}

func (s *flushingStore) CloseEntity(ctx context.Context, update projection.Update) (bool, error) {
	if _, err := s.db.Collection(update.Collection).UpdateOne(ctx, bson.D{{Key: "_id", Value: update.ID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "lastStatsAt", Value: s.at}}}}); err != nil {
		return false, err
	}
	return s.Store.CloseEntity(ctx, update)
}

func TestReconcilerUniqueIdentities(t *testing.T) {
	client := mongotest.StartMongo(t)
	env := newReconcilerEnv(t, client)
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

	env.reconcile()
	value := func(scope string, day time.Time) any {
		document := find[bson.M](env.testEnv, schema.CollectionAnalyticsDaily, scope+":"+day.Truncate(24*time.Hour).Format(time.RFC3339))
		return document["uniqueIdentities"]
	}
	require.Equal(t, []any{int32(3), int32(2), int32(2)}, []any{value("global", day1), value("node:ND_a", day1), value("node:ND_b", day1)})
	require.Equal(t, []any{int32(1), int32(1), int32(0)}, []any{value("global", day2), value("node:ND_a", day2), value("node:ND_b", day2)})
	require.Nil(t, value("global", reconcileNow), "the current day has no value until it closes")

	first := snapshot(env.testEnv, schema.CollectionAnalyticsDaily)
	env.reconcile()
	require.Equal(t, first, snapshot(env.testEnv, schema.CollectionAnalyticsDaily))
	require.NotEmpty(t, env.logs.matching("debug", "unique identities"))
}

func valueOf(n *int32) int32 {
	if n == nil {
		return 0
	}
	return *n
}
