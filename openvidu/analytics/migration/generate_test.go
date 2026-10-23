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

package migration

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Scenarios of the data generator of the OpenVidu Dashboard (dashboard-ce/tools/data-generator).
var legacyScenarios = map[string]struct{ roomsPerDay, avgRoomSize int }{
	"S": {200, 4},
	"M": {2000, 6},
	"L": {5000, 8},
}

const (
	generatedSessionMinutes = 20
	statsFlushSeconds       = 30
	legacyRetentionDays     = 32
	insertBatch             = 5000
)

// TestGenerateLegacyScenario fills a database with the collections the legacy writer stored for a data scenario:
// the protocol events and stats of the generated sessions, encoded as the legacy writer encoded them, with its indexes,
// plus the close events its fixer inserted for the sessions of crashed nodes. It runs only when
// MIGRATION_LEGACY_TARGET_URI, MIGRATION_LEGACY_TARGET_DB, MIGRATION_LEGACY_SCENARIO and MIGRATION_LEGACY_DAYS are set;
// MIGRATION_LEGACY_SEED (default 1) makes it reproducible.
func TestGenerateLegacyScenario(t *testing.T) {
	uri, database := os.Getenv("MIGRATION_LEGACY_TARGET_URI"), os.Getenv("MIGRATION_LEGACY_TARGET_DB")
	scenario, ok := legacyScenarios[os.Getenv("MIGRATION_LEGACY_SCENARIO")]
	days, _ := strconv.Atoi(os.Getenv("MIGRATION_LEGACY_DAYS"))
	if uri == "" || database == "" || !ok || days <= 0 {
		t.Skip("set MIGRATION_LEGACY_TARGET_URI, MIGRATION_LEGACY_TARGET_DB, MIGRATION_LEGACY_SCENARIO (S, M, L) and MIGRATION_LEGACY_DAYS")
	}
	seed, err := strconv.ParseInt(os.Getenv("MIGRATION_LEGACY_SEED"), 10, 64)
	if err != nil {
		seed = 1
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	db := client.Database(database)
	require.NoError(t, db.Drop(ctx))

	events, stats := newLegacyWriter(t, db.Collection("events")), newLegacyWriter(t, db.Collection("stats"))
	g := &legacyGenerator{t: t, rnd: rand.New(rand.NewSource(seed)), events: events, stats: stats,
		end: time.Now().UTC().Truncate(time.Minute)}
	start := g.end.Add(-time.Duration(days) * 24 * time.Hour)
	rooms := scenario.roomsPerDay * days
	for i := range rooms {
		created := start.Add(time.Duration(g.rnd.Int63n(int64(g.end.Sub(start) - generatedSessionMinutes*time.Minute))))
		g.room(i, created, scenario.avgRoomSize)
	}
	events.flush()
	stats.flush()

	for _, index := range []struct {
		collection string
		keys       bson.D
		ttl        bool
	}{
		{"events", bson.D{{Key: "type", Value: 1}}, false},
		{"events", bson.D{{Key: "room.sid", Value: 1}}, false},
		{"events", bson.D{{Key: "participant.sid", Value: 1}}, false},
		{"events", bson.D{{Key: "timestamp.seconds", Value: 1}}, false},
		{"events", bson.D{{Key: "participant_id", Value: 1}, {Key: "type", Value: 1}}, false},
		{"events", bson.D{{Key: "egress_id", Value: 1}, {Key: "type", Value: 1}}, false},
		{"events", bson.D{{Key: "room.sid", Value: 1}, {Key: "type", Value: 1}}, false},
		{"events", bson.D{{Key: "openvidu_expire_at", Value: 1}}, true},
		{"stats", bson.D{{Key: "time_stamp.seconds", Value: 1}}, false},
		{"stats", bson.D{{Key: "openvidu_expire_at", Value: 1}}, true},
	} {
		model := mongo.IndexModel{Keys: index.keys}
		if index.ttl {
			model.Options = options.Index().SetExpireAfterSeconds(0)
		}
		_, err := db.Collection(index.collection).Indexes().CreateOne(ctx, model)
		require.NoError(t, err)
	}
	require.NoError(t, db.CreateCollection(ctx, "active_entities"))
	_, err = db.Collection("last_alive").InsertOne(ctx, bson.D{{Key: "_id", Value: "server"},
		{Key: "last_alive", Value: bson.D{{Key: "seconds", Value: g.end.Unix()}}}})
	require.NoError(t, err)
	t.Logf("scenario %s, %d days: %d rooms, %d sessions, %d events (%d fixer), %d stats", os.Getenv("MIGRATION_LEGACY_SCENARIO"),
		days, rooms, g.sessions, events.count, g.fakes, stats.count)
}

type legacyWriter struct {
	t          *testing.T
	collection *mongo.Collection
	pending    []any
	count      int
}

func newLegacyWriter(t *testing.T, collection *mongo.Collection) *legacyWriter {
	return &legacyWriter{t: t, collection: collection}
}

func (w *legacyWriter) add(document bson.Raw) {
	w.pending = append(w.pending, document)
	w.count++
	if len(w.pending) >= insertBatch {
		w.flush()
	}
}

func (w *legacyWriter) flush() {
	if len(w.pending) == 0 {
		return
	}
	_, err := w.collection.InsertMany(context.Background(), w.pending, options.InsertMany().SetOrdered(false))
	var bulkErr mongo.BulkWriteException
	if errors.As(err, &bulkErr) && bulkErr.WriteConcernError == nil {
		// The legacy writer lost the events whose md5 _id collided too.
		for _, writeErr := range bulkErr.WriteErrors {
			require.Equal(w.t, 11000, writeErr.Code, writeErr.Message)
		}
		w.count -= len(bulkErr.WriteErrors)
		err = nil
	}
	require.NoError(w.t, err)
	w.pending = w.pending[:0]
}

type legacyGenerator struct {
	t        *testing.T
	rnd      *rand.Rand
	events   *legacyWriter
	stats    *legacyWriter
	end      time.Time
	sequence int
	sessions int
	fakes    int
}

type generatedParticipant struct {
	info   *livekit.ParticipantInfo
	joined time.Time
	left   time.Time
	tracks []*livekit.TrackInfo
}

func (g *legacyGenerator) id(prefix string) string {
	g.sequence++
	return fmt.Sprintf("%s_%011d", prefix, g.sequence)
}

func (g *legacyGenerator) event(eventType livekit.AnalyticsEventType, at time.Time, node string, fill func(*livekit.AnalyticsEvent)) {
	at = at.Add(time.Duration(g.rnd.Intn(1_000_000)) * time.Microsecond)
	event := &livekit.AnalyticsEvent{Id: g.id("AE"), Type: eventType, Timestamp: timestamppb.New(at), NodeId: node}
	fill(event)
	raw := legacyEventDocument(g.t, event)
	g.events.add(withExpiry(g.t, raw, at))
}

// withExpiry sets openvidu_expire_at as the legacy writer did: write time plus its 32-day retention.
func withExpiry(t *testing.T, raw bson.Raw, at time.Time) bson.Raw {
	var document bson.D
	require.NoError(t, bson.Unmarshal(raw, &document))
	for i := range document {
		if document[i].Key == "openvidu_expire_at" {
			document[i].Value = at.Add(legacyRetentionDays * 24 * time.Hour)
		}
	}
	out, err := bson.Marshal(document)
	require.NoError(t, err)
	return out
}

func (g *legacyGenerator) webhook(name string, room *livekit.Room, at time.Time, node string) {
	g.event(livekit.AnalyticsEventType_WEBHOOK, at, node, func(e *livekit.AnalyticsEvent) {
		e.Webhook = &livekit.WebhookInfo{Event: name, RoomName: room.Name, RoomId: room.Sid, EventId: g.id("EV"),
			CreatedAt: timestamppb.New(at), QueuedAt: timestamppb.New(at), SentAt: timestamppb.New(at),
			Url: "http://openvidu-meet:6080/livekit/webhook"}
	})
}

func (g *legacyGenerator) room(index int, created time.Time, avgSize int) {
	node := fmt.Sprintf("ND_node%07d", index%3)
	room := &livekit.Room{Sid: g.id("RM"), Name: fmt.Sprintf("room-%d", index), EmptyTimeout: 300, DepartureTimeout: 20,
		MaxParticipants: 100, CreationTime: created.Unix(), CreationTimeMs: created.UnixMilli(), TurnPassword: "SECRET-turn"}
	crashed := g.rnd.Intn(100) == 0
	g.event(livekit.AnalyticsEventType_ROOM_CREATED, created, node, func(e *livekit.AnalyticsEvent) { e.Room = room })
	g.webhook("room_started", room, created, node)

	size := max(1, avgSize+g.rnd.Intn(5)-2)
	var participants []*generatedParticipant
	roomEnd := created
	for p := range size {
		joined := created.Add(time.Duration(g.rnd.Intn(120)) * time.Second)
		duration := time.Duration(generatedSessionMinutes/2+g.rnd.Intn(generatedSessionMinutes)) * time.Minute
		participant := &generatedParticipant{joined: joined, left: joined.Add(duration), info: &livekit.ParticipantInfo{
			Sid: g.id("PA"), Identity: fmt.Sprintf("user-%d-%d", index, p), Name: fmt.Sprintf("User %d", p),
			JoinedAt: joined.Unix(), JoinedAtMs: joined.UnixMilli(), Region: "eu-west", IsPublisher: true,
			Permission: &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: true, CanPublishData: true},
		}}
		participant.tracks = []*livekit.TrackInfo{
			{Sid: g.id("TR"), Type: livekit.TrackType_AUDIO, Source: livekit.TrackSource_MICROPHONE, MimeType: "audio/opus"},
			{Sid: g.id("TR"), Type: livekit.TrackType_VIDEO, Source: livekit.TrackSource_CAMERA, MimeType: "video/VP8",
				Width: 1280, Height: 720, Simulcast: true},
		}
		participants = append(participants, participant)
		if participant.left.After(roomEnd) {
			roomEnd = participant.left
		}
		g.sessions++
	}

	for _, participant := range participants {
		info := participant.info
		g.event(livekit.AnalyticsEventType_PARTICIPANT_JOINED, participant.joined, node, func(e *livekit.AnalyticsEvent) {
			e.RoomId, e.Room, e.ParticipantId, e.Participant = room.Sid, room, info.Sid, info
			e.ClientInfo = &livekit.ClientInfo{Sdk: livekit.ClientInfo_JS, Browser: "Chrome", Os: "Windows", Address: "198.51.100.7"}
		})
		g.webhook("participant_joined", room, participant.joined, node)
		active := participant.joined.Add(time.Second)
		g.event(livekit.AnalyticsEventType_PARTICIPANT_ACTIVE, active, node, func(e *livekit.AnalyticsEvent) {
			e.RoomId, e.Room, e.ParticipantId, e.Participant = room.Sid, room, info.Sid, info
			e.ClientMeta = &livekit.AnalyticsClientMeta{ClientConnectTime: 900, ConnectionType: "udp"}
		})
		for _, track := range participant.tracks {
			published := active.Add(time.Second)
			g.event(livekit.AnalyticsEventType_TRACK_PUBLISHED, published, node, func(e *livekit.AnalyticsEvent) {
				e.RoomId, e.Room, e.ParticipantId, e.Participant = room.Sid, room, info.Sid, info
				e.TrackId, e.Track = track.Sid, track
			})
			g.webhook("track_published", room, published, node)
		}
	}
	for _, subscriber := range participants {
		for _, publisher := range participants {
			if publisher == subscriber {
				continue
			}
			from, to := maxTime(subscriber.joined, publisher.joined).Add(3*time.Second), minTime(subscriber.left, publisher.left)
			if !to.After(from) {
				continue
			}
			for _, track := range publisher.tracks {
				g.event(livekit.AnalyticsEventType_TRACK_SUBSCRIBED, from, node, func(e *livekit.AnalyticsEvent) {
					e.RoomId, e.Room, e.ParticipantId = room.Sid, room, subscriber.info.Sid
					e.Participant = &livekit.ParticipantInfo{Sid: subscriber.info.Sid}
					e.TrackId, e.Track = track.Sid, track
					e.Publisher = &livekit.ParticipantInfo{Sid: publisher.info.Sid, Identity: publisher.info.Identity}
				})
				if !crashed {
					g.event(livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED, to, node, func(e *livekit.AnalyticsEvent) {
						e.RoomId, e.Room, e.ParticipantId = room.Sid, room, subscriber.info.Sid
						e.Participant = &livekit.ParticipantInfo{Sid: subscriber.info.Sid}
						e.TrackId, e.Track = track.Sid, &livekit.TrackInfo{Sid: track.Sid, Type: track.Type}
					})
				}
				g.flushes(room, subscriber, track, livekit.StreamType_DOWNSTREAM, from, to, node)
			}
		}
	}
	for _, participant := range participants {
		info := participant.info
		for _, track := range participant.tracks {
			g.flushes(room, participant, track, livekit.StreamType_UPSTREAM, participant.joined, participant.left, node)
		}
		if crashed {
			continue
		}
		for _, track := range participant.tracks {
			g.event(livekit.AnalyticsEventType_TRACK_UNPUBLISHED, participant.left, node, func(e *livekit.AnalyticsEvent) {
				e.RoomId, e.Room, e.ParticipantId = room.Sid, room, info.Sid
				e.Participant = &livekit.ParticipantInfo{Sid: info.Sid}
				e.TrackId, e.Track = track.Sid, track
			})
		}
		left := cloneParticipant(info)
		left.State = livekit.ParticipantInfo_DISCONNECTED
		left.DisconnectReason = livekit.DisconnectReason_CLIENT_INITIATED
		g.event(livekit.AnalyticsEventType_PARTICIPANT_LEFT, participant.left, node, func(e *livekit.AnalyticsEvent) {
			e.RoomId, e.Room, e.ParticipantId, e.Participant = room.Sid, room, info.Sid, left
		})
		g.webhook("participant_left", room, participant.left, node)
	}

	if g.rnd.Intn(10) == 0 {
		g.egress(room, created.Add(10*time.Second), roomEnd, node, crashed)
	}
	if crashed {
		// The node died: the fixer closed the sessions at its last_alive, a while after the crash.
		lastAlive := roomEnd.Add(-5 * time.Minute)
		for _, participant := range participants {
			g.fixerLeft(room, participant, lastAlive)
		}
		g.fixerRoomEnded(room, lastAlive.Add(20*time.Second))
		return
	}
	g.event(livekit.AnalyticsEventType_ROOM_ENDED, roomEnd.Add(20*time.Second), node, func(e *livekit.AnalyticsEvent) {
		e.RoomId, e.Room = room.Sid, room
		e.RoomEndReason = livekit.RoomEndReason_ROOM_END_IDLE_TIMEOUT
	})
	g.webhook("room_finished", room, roomEnd.Add(20*time.Second), node)
}

func (g *legacyGenerator) egress(room *livekit.Room, started time.Time, ended time.Time, node string, crashed bool) {
	request := &livekit.EgressInfo_RoomComposite{RoomComposite: &livekit.RoomCompositeEgressRequest{RoomName: room.Name,
		Layout: "grid", Options: &livekit.RoomCompositeEgressRequest_Preset{Preset: livekit.EncodingOptionsPreset_H264_720P_30},
		FileOutputs: []*livekit.EncodedFileOutput{{Filepath: "recordings/" + room.Name + ".mp4"}}}}
	info := &livekit.EgressInfo{EgressId: g.id("EG"), RoomId: room.Sid, RoomName: room.Name, Request: request,
		Status: livekit.EgressStatus_EGRESS_STARTING, StartedAt: started.UnixNano(), UpdatedAt: started.UnixNano()}
	g.event(livekit.AnalyticsEventType_EGRESS_STARTED, started, node, func(e *livekit.AnalyticsEvent) {
		e.EgressId, e.RoomId, e.Egress = info.EgressId, room.Sid, info
	})
	if crashed {
		return
	}
	done := cloneEgress(info)
	done.Status, done.EndedAt, done.UpdatedAt = livekit.EgressStatus_EGRESS_COMPLETE, ended.UnixNano(), ended.UnixNano()
	g.event(livekit.AnalyticsEventType_EGRESS_ENDED, ended, node, func(e *livekit.AnalyticsEvent) {
		e.EgressId, e.RoomId, e.Egress = info.EgressId, room.Sid, done
	})
}

// flushes writes the stats of one track and direction every 30 s of the interval.
func (g *legacyGenerator) flushes(room *livekit.Room, participant *generatedParticipant, track *livekit.TrackInfo,
	kind livekit.StreamType, from time.Time, to time.Time, node string) {
	for at := from.Add(statsFlushSeconds * time.Second); !at.After(to); at = at.Add(statsFlushSeconds * time.Second) {
		bytes := uint64(40_000 + g.rnd.Intn(20_000))
		if track.Type == livekit.TrackType_VIDEO {
			bytes *= 20
		}
		stat := &livekit.AnalyticsStat{Id: g.id("AS"), Kind: kind, TimeStamp: timestamppb.New(at), Node: node, RoomId: room.Sid,
			RoomName: room.Name, ParticipantId: participant.info.Sid, TrackId: track.Sid, Score: 4.2, MinScore: 3.9,
			MedianScore: 4.2, Mime: track.MimeType, Streams: []*livekit.AnalyticsStream{{PrimaryBytes: bytes,
				PrimaryPackets: uint32(bytes / 1000), PacketsLost: uint32(g.rnd.Intn(5)), Rtt: 40, Jitter: 2500}}}
		g.stats.add(withExpiry(g.t, legacyStatDocument(g.t, stat), at))
	}
}

func (g *legacyGenerator) fixerLeft(room *livekit.Room, participant *generatedParticipant, at time.Time) {
	g.fake(at, bson.D{{Key: "type", Value: "PARTICIPANT_LEFT"},
		{Key: "room", Value: bson.D{{Key: "sid", Value: room.Sid}}}, {Key: "room_id", Value: room.Sid},
		{Key: "participant_id", Value: participant.info.Sid},
		{Key: "participant", Value: bson.D{{Key: "sid", Value: participant.info.Sid},
			{Key: "identity", Value: participant.info.Identity}, {Key: "joined_at", Value: float64(participant.info.JoinedAt)}}}})
}

func (g *legacyGenerator) fixerRoomEnded(room *livekit.Room, at time.Time) {
	g.fake(at, bson.D{{Key: "type", Value: "ROOM_ENDED"}, {Key: "room_id", Value: room.Sid},
		{Key: "room", Value: bson.D{{Key: "sid", Value: room.Sid}, {Key: "name", Value: room.Name},
			{Key: "creation_time", Value: float64(room.CreationTime)}}}})
}

func (g *legacyGenerator) fake(at time.Time, fields bson.D) {
	document := append(bson.D{{Key: "_id", Value: bson.NewObjectID()},
		{Key: "timestamp", Value: bson.D{{Key: "seconds", Value: at.Unix()}, {Key: "nanos", Value: int32(at.Nanosecond())}}},
		{Key: "openvidu_expire_at", Value: at.Add(legacyRetentionDays * 24 * time.Hour)}}, fields...)
	raw, err := bson.Marshal(document)
	require.NoError(g.t, err)
	g.events.add(raw)
	g.fakes++
}

func maxTime(a time.Time, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a time.Time, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
