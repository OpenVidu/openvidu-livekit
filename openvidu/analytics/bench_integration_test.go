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
	"os"
	"slices"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
)

const (
	benchEvents  = 1000
	benchStats   = 5000
	benchBatches = 20
	// Performance budget of a batch: p95 latency, and round trips with every entity
	// and rollup collection touched.
	benchMaxP95        = 2 * time.Second
	benchMaxRoundTrips = 20
)

// TestBatchPerformanceBudget applies batches of a node at scale L against a database filled by the data generator of
// the OpenVidu Dashboard (dashboard-ce/tools/data-generator, scenario M, 7 days). It runs only when ANALYTICS_BENCH_MONGO_URI and ANALYTICS_BENCH_DB
// name that database.
func TestBatchPerformanceBudget(t *testing.T) {
	uri, database := os.Getenv("ANALYTICS_BENCH_MONGO_URI"), os.Getenv("ANALYTICS_BENCH_DB")
	if uri == "" || database == "" {
		t.Skip("set ANALYTICS_BENCH_MONGO_URI and ANALYTICS_BENCH_DB to a generated dataset")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	db := client.Database(database)

	for _, detail := range []string{"participant", "track"} {
		t.Run(detail, func(t *testing.T) {
			conf := openviduconfig.AnalyticsConfig{Enabled: true, MongoUrl: uri, StatsDetail: detail}
			settings, _, err := conf.Settings()
			require.NoError(t, err)
			_, err = schema.Initialize(context.Background(), db, schema.InitOptions{
				ValidationAction: "warn", Retention: retentionOf(settings), StatsDetail: detail, WriterVersion: "bench",
			})
			require.NoError(t, err)
			s := store.New(db)
			p := &processor{store: s, opts: projectionOptions(settings), cache: newRefCache()}

			var durations []time.Duration
			var roundTrips []int
			for i := range benchBatches {
				events, stats := benchBatch(fmt.Sprintf("%s%02d", detail[:1], i), time.Now().UTC().Add(-time.Minute))
				s.ResetRoundTrips()
				started := time.Now()
				require.NoError(t, p.apply(context.Background(), entriesOf(events), []*statsEntry{{stats: stats}}))
				durations = append(durations, time.Since(started))
				roundTrips = append(roundTrips, s.RoundTrips())
			}
			slices.Sort(durations)
			p50, p95 := durations[len(durations)/2], durations[len(durations)*95/100]
			t.Logf("%d events + %d stats, %s detail: p50 %s, p95 %s, max %s, round trips %d–%d", benchEvents, benchStats,
				detail, p50, p95, durations[len(durations)-1], slices.Min(roundTrips), slices.Max(roundTrips))
			require.LessOrEqual(t, p95, benchMaxP95)
			require.LessOrEqual(t, slices.Max(roundTrips), benchMaxRoundTrips)
		})
	}
}

func entriesOf(events []*livekit.AnalyticsEvent) []*eventEntry {
	entries := make([]*eventEntry, len(events))
	for i, event := range events {
		entries[i] = &eventEntry{event: event}
	}
	return entries
}

// benchBatch builds benchEvents events of rooms of five publishers that subscribe to each other, with every second
// room ended, and benchStats stats of their streams; prefix keeps the IDs of each batch apart.
func benchBatch(prefix string, start time.Time) ([]*livekit.AnalyticsEvent, []*livekit.AnalyticsStat) {
	var ev events
	type stream struct {
		room, participant, track string
		kind                     livekit.StreamType
	}
	var batch []*livekit.AnalyticsEvent
	var streams []stream
	offset := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }

	for room := 0; len(batch) < benchEvents; room++ {
		roomSid := fmt.Sprintf("RM_b%s%04d", prefix, room)
		batch = append(batch, ev.roomCreated(roomSid, "bench-"+roomSid, offset(0)))
		var participants []*livekit.ParticipantInfo
		var tracks [][2]*livekit.TrackInfo
		for p := range 5 {
			info := participantInfo(fmt.Sprintf("PA_b%s%04d%d", prefix, room, p), fmt.Sprintf("user-%d", p),
				livekit.ParticipantInfo_STANDARD, offset(1))
			camera, mic := cameraTrack(fmt.Sprintf("TR_c%s%04d%d", prefix, room, p)), microphoneTrack(fmt.Sprintf("TR_m%s%04d%d", prefix, room, p))
			participants = append(participants, info)
			tracks = append(tracks, [2]*livekit.TrackInfo{camera, mic})
			batch = append(batch, ev.joined(roomSid, info, offset(1)), ev.active(roomSid, info, offset(2)),
				ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, roomSid, info, camera, offset(3)),
				ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, roomSid, info, mic, offset(3)))
		}
		for s, subscriber := range participants {
			for p, publisher := range participants {
				if s == p {
					continue
				}
				for _, track := range tracks[p] {
					batch = append(batch, ev.subscription(livekit.AnalyticsEventType_TRACK_SUBSCRIBED, roomSid,
						subscriber.GetSid(), track, publisher, offset(4)))
					streams = append(streams, stream{roomSid, subscriber.GetSid(), track.GetSid(), livekit.StreamType_DOWNSTREAM})
				}
			}
			for _, track := range tracks[s] {
				streams = append(streams, stream{roomSid, subscriber.GetSid(), track.GetSid(), livekit.StreamType_UPSTREAM})
			}
		}
		if room%2 == 1 {
			for _, info := range participants {
				batch = append(batch, ev.left(roomSid, info, offset(50), livekit.DisconnectReason_CLIENT_INITIATED))
			}
			batch = append(batch, ev.roomEnded(roomSid, "bench-"+roomSid, offset(0), offset(55), livekit.RoomEndReason_ROOM_END_IDLE_TIMEOUT))
		}
	}
	batch = batch[:benchEvents]

	// Streams report every 30 seconds until the batch holds benchStats stats.
	stats := make([]*livekit.AnalyticsStat, benchStats)
	for i := range stats {
		st := streams[i%len(streams)]
		stats[i] = stat(st.room, st.participant, st.track, st.kind, offset(30*(1+i/len(streams))), 4.4, 250000)
	}
	return batch, stats
}
