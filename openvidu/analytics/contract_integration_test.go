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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/protojson"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	"github.com/livekit/livekit-server/openvidu/analytics/testutil"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
)

// The canonical scenario of the data model contract (dashboard-ce/typings/contract/fixtures/scenario.ts): one closed
// room on node alpha (09:00–09:45 UTC) and one active room on node beta (since 10:00 UTC) on 2026-10-12. The raw
// events of the vendored fixtures are replayed as recorded; the other events and the stats that the documents
// imply are built here. The leave event of the agent is lost: the reconciler closes it, and computes the unique
// identities of the day once it has closed.

const (
	scNodeAlpha     = "ND_alpha8Kq2Lm"
	scNodeBeta      = "ND_beta3Vx9Pw"
	scServerVersion = "3.10.0"
	scRegion        = "eu-west"
	scClosedRoom    = "RM_aLkq3ZpD9wXy"
	scClosedName    = "Daily-Standup"
	scActiveRoom    = "RM_bQ7tVnW2cErT"
	scActiveName    = "Webinar-Q4"

	scAlice     = "PA_aL1ce7Hd2Kq9"
	scBob1      = "PA_b0bFirst4Tz1"
	scBob2      = "PA_b0bSecnd8Rw3"
	scCarol     = "PA_car0lAb0rt5n"
	scRecorder  = "PA_egRec0rder01"
	scAgent     = "PA_ag3ntAssist1"
	scSIP       = "PA_s1pCaller7Yx"
	scDave      = "PA_dav3Pub6Mn2"
	scErin      = "PA_er1nJ0in9Qa"
	scIngress1  = "PA_1ngressSes01"
	scIngress2  = "PA_1ngressSes02"
	scStreamer  = "PA_egStr3amer02"
	scAliceCam  = "TR_aLiceCam3Vp8"
	scAliceMic  = "TR_aLiceMic0pus"
	scDaveCam   = "TR_daveCamH264x"
	scDaveMic   = "TR_daveMic0pusx"
	scIngVideo1 = "TR_1ngVid1H264a"
	scIngVideo2 = "TR_1ngVid2H264b"
	scRecording = "EG_rec0rdMp4Kq2"
	scFailed    = "EG_trackF4iled9"
	scStreaming = "EG_streamHls7Wz"
	scRTMP      = "IN_0bsRtmpReu5e"
	scWHIP      = "IN_wh1pDeleted3"
	scSession1  = "rtmp_7f3c9a1e2b4d"
	scSession2  = "rtmp_9d4e2f6a8c1b"
)

// sat returns an instant of the scenario day, "HH:MM:SS[.mmm]" UTC.
func sat(clock string) time.Time {
	t, err := time.Parse("2006-01-02T15:04:05.000Z", "2026-10-12T"+clock+strings.Repeat(".000", 1-strings.Count(clock, "."))+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

func TestContractFixtures(t *testing.T) {
	client := testutil.StartMongo(t)
	ctx := context.Background()

	t.Run("participant detail", func(t *testing.T) {
		db := contractDatabase(t, client, "contract_participant")
		p := contractProcessor(t, db, "participant")
		replayScenario(t, p, contractEvents(t), contractStats())
		s := store.New(db)
		for _, node := range []struct {
			id        string
			firstSeen time.Time
		}{{scNodeAlpha, time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}, {scNodeBeta, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}} {
			require.NoError(t, s.Heartbeat(ctx, node.id, scServerVersion, scRegion, 10*time.Second, node.firstSeen))
			require.NoError(t, s.Heartbeat(ctx, node.id, scServerVersion, scRegion, 10*time.Second, sat("10:35:10")))
		}
		reconcileScenario(t, db, p.opts)

		for _, collection := range []string{
			schema.CollectionRooms, schema.CollectionParticipants, schema.CollectionTracks,
			schema.CollectionTrackSubscriptions, schema.CollectionEgresses, schema.CollectionIngresses,
			schema.CollectionIngressSessions, schema.CollectionNodes,
		} {
			compareCollection(t, db, collection, true)
		}
		for _, collection := range []string{
			schema.CollectionEvents, schema.CollectionAnalytics5Min, schema.CollectionAnalyticsHourly,
			schema.CollectionAnalyticsDaily, schema.CollectionSchemaMeta,
		} {
			compareCollection(t, db, collection, false)
		}
		compareTrackStats(t, db, "")
	})

	t.Run("track detail", func(t *testing.T) {
		db := contractDatabase(t, client, "contract_track")
		p := contractProcessor(t, db, "track")
		var lastFlush []*livekit.AnalyticsStat
		for _, flush := range contractStats() {
			if flush.at.Equal(sat("10:35:00")) {
				lastFlush = flush.stats
			}
		}
		require.NoError(t, p.apply(ctx, nil, []*statsEntry{{stats: lastFlush}}))
		compareTrackStats(t, db, "track")
	})
}

// reconcileScenario runs the reconciler twice at 10:35:20, with LiveKit listing the active room and its participants
// only, which closes the agent of the closed room, then once after the day has closed.
func reconcileScenario(t *testing.T, db *mongo.Database, opts projection.Options) {
	live := newFakeLive()
	live.addRoom(scActiveName, scActiveRoom, scDave, scErin, scIngress2, scStreamer)
	r := newReconciler(store.New(db), live, opts, reconcilerSettings{batchInterval: 10 * time.Second,
		interval: time.Minute, cleanupInterval: 3 * time.Minute, maxClosures: 500, entitiesRetention: 2160 * time.Hour},
		scNodeAlpha, buildReconcilerMetrics())
	r.log = (&capturedLog{}).sink()
	now := sat("10:35:20")
	r.now = func() time.Time { return now }
	for range 2 {
		require.NoError(t, r.reconcile(context.Background(), false).err)
	}
	now = time.Date(2026, 10, 13, 0, 10, 0, 0, time.UTC)
	require.NoError(t, r.dailyUniqueIdentities(context.Background(), now))
}

func contractDatabase(t *testing.T, client *mongo.Client, name string) *mongo.Database {
	db := client.Database(name)
	require.NoError(t, db.Drop(context.Background()))
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	return db
}

func contractProcessor(t *testing.T, db *mongo.Database, detail string) *processor {
	// The canonical fixtures carry client addresses.
	storeIP := false
	settings := testSettings(t, func(c *openviduconfig.AnalyticsConfig) {
		c.StatsDetail = detail
		c.AnonymizeIP = &storeIP
	})
	_, err := schema.Initialize(context.Background(), db, schema.InitOptions{
		ValidationAction: "error", Retention: retentionOf(settings), StatsDetail: detail, WriterVersion: scServerVersion,
		Now: func() time.Time { return sat("08:00:00") },
	})
	require.NoError(t, err)
	return &processor{store: store.New(db), opts: projectionOptions(settings), cache: newRefCache(),
		onWriteErrors: func(err *store.WriteError) { t.Errorf("write rejected: %v", err) }}
}

type statsFlush struct {
	at    time.Time
	stats []*livekit.AnalyticsStat
}

// replayScenario applies the events and stats in 10-second batches, as a node does.
func replayScenario(t *testing.T, p *processor, events []*livekit.AnalyticsEvent, flushes []statsFlush) {
	window := func(ts time.Time) time.Time { return ts.Truncate(10 * time.Second) }
	windows := map[time.Time]bool{}
	for _, event := range events {
		windows[window(event.GetTimestamp().AsTime())] = true
	}
	for _, flush := range flushes {
		windows[window(flush.at)] = true
	}
	var ordered []time.Time
	for w := range windows {
		ordered = append(ordered, w)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Before(ordered[j]) })

	for _, w := range ordered {
		var entries []*eventEntry
		for _, event := range events {
			if window(event.GetTimestamp().AsTime()).Equal(w) {
				entries = append(entries, &eventEntry{event: event})
			}
		}
		var stats []*statsEntry
		for _, flush := range flushes {
			if window(flush.at).Equal(w) {
				stats = append(stats, &statsEntry{stats: flush.stats})
			}
		}
		require.NoError(t, p.apply(context.Background(), entries, stats), "batch at %s", w)
	}
}

// --- Events -------------------------------------------------------------------------------------------------------

func contractEvents(t *testing.T) []*livekit.AnalyticsEvent {
	recorded, err := schema.Fixtures(schema.CollectionEvents)
	require.NoError(t, err)
	var events []*livekit.AnalyticsEvent
	for _, raw := range recorded {
		if raw.Lookup("synthetic").Boolean() {
			continue
		}
		events = append(events, eventFromFixture(t, raw))
	}
	events = append(events, scenarioEvents()...)
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].GetTimestamp().AsTime().Before(events[j].GetTimestamp().AsTime())
	})
	return events
}

// eventFromFixture rebuilds the protocol event of a stored raw event: the payload is the allow-listed event with
// oneof members nested under the oneof name, and the lifted keys come back as the event fields they were read from.
func eventFromFixture(t *testing.T, raw bson.Raw) *livekit.AnalyticsEvent {
	var document bson.D
	require.NoError(t, bson.Unmarshal(raw, &document))
	fields := map[string]any{}
	for _, element := range document {
		fields[element.Key] = element.Value
	}
	payload, ok := fields["payload"].(bson.D)
	require.True(t, ok)

	descriptor := (&livekit.AnalyticsEvent{}).ProtoReflect().Descriptor()
	message := protojsonFields(t, payload, descriptor)
	message["id"] = fields["_id"]
	message["type"] = fields["type"]
	message["timestamp"] = fields["ts"].(bson.DateTime).Time().UTC().Format(time.RFC3339Nano)
	message["node_id"] = fields["nodeId"]
	eventType := fields["type"].(string)
	if sid, ok := fields["roomSid"].(string); ok && eventType != "ROOM_CREATED" && !strings.HasPrefix(eventType, "INGRESS_") {
		message["room_id"] = sid
	}
	for lifted, field := range map[string]string{"participantSid": "participant_id", "trackSid": "track_id",
		"egressId": "egress_id", "ingressId": "ingress_id"} {
		if value, ok := fields[lifted].(string); ok {
			message[field] = value
		}
	}

	data, err := json.Marshal(message)
	require.NoError(t, err)
	event := &livekit.AnalyticsEvent{}
	require.NoError(t, protojson.UnmarshalOptions{}.Unmarshal(data, event), "%s", data)
	return event
}

func protojsonFields(t *testing.T, document bson.D, descriptor protoreflect.MessageDescriptor) map[string]any {
	out := map[string]any{}
	for _, element := range document {
		if field := descriptor.Fields().ByName(protoreflect.Name(element.Key)); field != nil {
			out[element.Key] = protojsonValue(t, element.Value, field)
			continue
		}
		require.NotNil(t, descriptor.Oneofs().ByName(protoreflect.Name(element.Key)), "%s.%s", descriptor.FullName(), element.Key)
		member := element.Value.(bson.D)
		require.Len(t, member, 1)
		field := descriptor.Fields().ByName(protoreflect.Name(member[0].Key))
		require.NotNil(t, field, "%s.%s", descriptor.FullName(), member[0].Key)
		out[member[0].Key] = protojsonValue(t, member[0].Value, field)
	}
	return out
}

func protojsonValue(t *testing.T, value any, field protoreflect.FieldDescriptor) any {
	switch {
	case field.IsList():
		var items []any
		for _, item := range value.(bson.A) {
			items = append(items, protojsonSingular(t, item, field))
		}
		return items
	case field.IsMap():
		entries := map[string]any{}
		for _, element := range value.(bson.D) {
			entries[element.Key] = protojsonSingular(t, element.Value, field.MapValue())
		}
		return entries
	default:
		return protojsonSingular(t, value, field)
	}
}

func protojsonSingular(t *testing.T, value any, field protoreflect.FieldDescriptor) any {
	if field.Kind() == protoreflect.MessageKind {
		if date, ok := value.(bson.DateTime); ok {
			return date.Time().UTC().Format(time.RFC3339Nano)
		}
		return protojsonFields(t, value.(bson.D), field.Message())
	}
	switch v := value.(type) {
	case bson.Binary:
		return base64.StdEncoding.EncodeToString(v.Data)
	default:
		return v
	}
}

func stamp(event *livekit.AnalyticsEvent, id string, eventType livekit.AnalyticsEventType, node string, at time.Time) *livekit.AnalyticsEvent {
	event.Id = id
	event.Type = eventType
	event.NodeId = node
	event.Timestamp = timestamppb.New(at)
	return event
}

func scRoom(sid string) *livekit.Room {
	if sid == scClosedRoom {
		return &livekit.Room{Sid: sid, Name: scClosedName}
	}
	return &livekit.Room{Sid: sid, Name: scActiveName}
}

func scNode(room string) string {
	if room == scClosedRoom {
		return scNodeAlpha
	}
	return scNodeBeta
}

type scPerson struct {
	sid, identity, name string
	kind                livekit.ParticipantInfo_Kind
	room                string
	joined              time.Time
	permission          *livekit.ParticipantPermission
	client              *livekit.ClientInfo
}

var (
	scStandardPermission = &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: true, CanPublishData: true}
	scRecorderPermission = &livekit.ParticipantPermission{CanSubscribe: true, Hidden: true}
	scIngressPermission  = &livekit.ParticipantPermission{CanPublish: true, CanPublishData: true}
	scSafari             = &livekit.ClientInfo{Sdk: livekit.ClientInfo_JS, Version: "2.15.7", Os: "macOS", OsVersion: "15.1",
		Browser: "Safari", BrowserVersion: "18.1", Network: "wired", Address: "198.51.100.24"}
	scGoClient = &livekit.ClientInfo{Sdk: livekit.ClientInfo_GO, Version: "2.11.0", Os: "linux"}
)

func (p scPerson) info(state livekit.ParticipantInfo_State) *livekit.ParticipantInfo {
	return &livekit.ParticipantInfo{
		Sid: p.sid, Identity: p.identity, Name: p.name, Kind: p.kind, State: state,
		JoinedAt: p.joined.Unix(), JoinedAtMs: p.joined.UnixMilli(), Permission: p.permission, Region: scRegion,
	}
}

func (p scPerson) joinedEvent(id string) *livekit.AnalyticsEvent {
	event := &livekit.AnalyticsEvent{RoomId: p.room, Room: scRoom(p.room), ParticipantId: p.sid,
		Participant: p.info(livekit.ParticipantInfo_JOINED), ClientInfo: p.client,
		ClientMeta: &livekit.AnalyticsClientMeta{Region: scRegion, Node: scNode(p.room)}}
	return stamp(event, id, livekit.AnalyticsEventType_PARTICIPANT_JOINED, scNode(p.room), p.joined)
}

func (p scPerson) activeEvent(id string, at time.Time, connectMs uint32, connection string) *livekit.AnalyticsEvent {
	event := &livekit.AnalyticsEvent{RoomId: p.room, Room: scRoom(p.room), ParticipantId: p.sid,
		Participant: p.info(livekit.ParticipantInfo_ACTIVE),
		ClientMeta:  &livekit.AnalyticsClientMeta{ClientConnectTime: connectMs, ConnectionType: connection}}
	return stamp(event, id, livekit.AnalyticsEventType_PARTICIPANT_ACTIVE, scNode(p.room), at)
}

func (p scPerson) leftEvent(id string, at time.Time, reason livekit.DisconnectReason, publisher bool) *livekit.AnalyticsEvent {
	info := p.info(livekit.ParticipantInfo_DISCONNECTED)
	info.DisconnectReason = reason
	info.IsPublisher = publisher
	event := &livekit.AnalyticsEvent{RoomId: p.room, Room: scRoom(p.room), ParticipantId: p.sid, Participant: info}
	return stamp(event, id, livekit.AnalyticsEventType_PARTICIPANT_LEFT, scNode(p.room), at)
}

func scTrackEvent(id string, eventType livekit.AnalyticsEventType, room string, participant *livekit.ParticipantInfo,
	track *livekit.TrackInfo, at time.Time) *livekit.AnalyticsEvent {
	event := &livekit.AnalyticsEvent{RoomId: room, Room: scRoom(room), ParticipantId: participant.GetSid(),
		Participant: participant, TrackId: track.GetSid(), Track: track}
	return stamp(event, id, eventType, scNode(room), at)
}

func scSubscription(id string, eventType livekit.AnalyticsEventType, room string, subscriber string,
	track *livekit.TrackInfo, publisher *livekit.ParticipantInfo, at time.Time) *livekit.AnalyticsEvent {
	event := &livekit.AnalyticsEvent{RoomId: room, Room: scRoom(room), ParticipantId: subscriber,
		Participant: &livekit.ParticipantInfo{Sid: subscriber}, TrackId: track.GetSid(), Track: track, Publisher: publisher}
	return stamp(event, id, eventType, scNode(room), at)
}

// scRTPStats mirrors rtpStats of the typings fixtures; jitter is in microseconds, as the protocol reports it.
func scRTPStats(start time.Time, end time.Time, packets uint32, bytes uint64, lost uint32, frames uint32, plis uint32) *livekit.RTPStats {
	seconds := end.Sub(start).Seconds()
	return &livekit.RTPStats{
		StartTime: timestamppb.New(start), EndTime: timestamppb.New(end), Duration: seconds,
		Packets: packets, PacketRate: math.Round(float64(packets)/seconds*100) / 100, Bytes: bytes,
		Bitrate: math.Round(float64(bytes)*8/seconds*100) / 100, PacketsLost: lost,
		PacketLossRate:       math.Round(float64(lost)/seconds*1000) / 1000,
		PacketLossPercentage: float32(math.Round(float64(lost)/float64(packets)*10000) / 100),
		Frames:               frames, JitterCurrent: 4200, JitterMax: 12500, Nacks: uint32(math.Round(float64(lost) / 3)),
		Plis: plis, RttCurrent: 40, RttMax: 85,
	}
}

func scStatsEvent(id string, eventType livekit.AnalyticsEventType, room string, participant string, track string,
	mime string, layer int32, stats *livekit.RTPStats, at time.Time) *livekit.AnalyticsEvent {
	event := &livekit.AnalyticsEvent{RoomId: room, Room: scRoom(room), ParticipantId: participant, TrackId: track,
		Mime: mime, VideoLayer: layer, RtpStats: stats}
	return stamp(event, id, eventType, scNode(room), at)
}

func scAPICall(id string, node string, service string, method string, status int32, room string, at time.Time) *livekit.AnalyticsEvent {
	call := &livekit.APICallInfo{Service: service, Method: method, NodeId: node, Status: status, RoomName: room,
		StartedAt: timestamppb.New(at), DurationNs: 2_000_000}
	if status >= 400 {
		call.TwirpErrorCode = "not_found"
		call.TwirpErrorMessage = "requested room does not exist"
	}
	return stamp(&livekit.AnalyticsEvent{ApiCall: call}, id, livekit.AnalyticsEventType_API_CALL, node, at)
}

func scEgress(id string, eventType livekit.AnalyticsEventType, info *livekit.EgressInfo, at time.Time) *livekit.AnalyticsEvent {
	event := &livekit.AnalyticsEvent{EgressId: info.GetEgressId(), RoomId: info.GetRoomId(), Egress: info}
	return stamp(event, id, eventType, scNode(info.GetRoomId()), at)
}

func scIngress(id string, eventType livekit.AnalyticsEventType, info *livekit.IngressInfo, at time.Time) *livekit.AnalyticsEvent {
	event := &livekit.AnalyticsEvent{IngressId: info.GetIngressId(), Ingress: info}
	return stamp(event, id, eventType, scNodeBeta, at)
}

func scenarioEvents() []*livekit.AnalyticsEvent {
	alice := scPerson{sid: scAlice, identity: "alice", name: "Alice", room: scClosedRoom, joined: sat("09:00:05"),
		permission: scStandardPermission}
	bob1 := scPerson{sid: scBob1, identity: "bob", name: "Bob", room: scClosedRoom, joined: sat("09:01:00"),
		permission: scStandardPermission, client: scSafari}
	bob2 := scPerson{sid: scBob2, identity: "bob", name: "Bob", room: scClosedRoom, joined: sat("09:10:02"),
		permission: scStandardPermission, client: scSafari}
	carol := scPerson{sid: scCarol, identity: "Carol", room: scClosedRoom, joined: sat("09:05:00"),
		permission: scStandardPermission, client: &livekit.ClientInfo{Sdk: livekit.ClientInfo_JS, Version: "2.15.7",
			Os: "Linux", Browser: "Chrome", BrowserVersion: "129.0.0.0", Network: "wired", Address: "192.0.2.77"}}
	recorder := scPerson{sid: scRecorder, identity: scRecording, kind: livekit.ParticipantInfo_EGRESS, room: scClosedRoom,
		joined: sat("09:02:00"), permission: scRecorderPermission, client: scGoClient}
	agent := scPerson{sid: scAgent, identity: "agent-assistant", name: "Assistant", kind: livekit.ParticipantInfo_AGENT,
		room: scClosedRoom, joined: sat("09:00:10"), permission: scStandardPermission,
		client: &livekit.ClientInfo{Sdk: livekit.ClientInfo_PYTHON, Version: "1.2.0", Os: "linux"}}
	sip := scPerson{sid: scSIP, identity: "sip-caller-1", name: "Caller", kind: livekit.ParticipantInfo_SIP,
		room: scClosedRoom, joined: sat("09:20:00"), permission: scStandardPermission}
	dave := scPerson{sid: scDave, identity: "dave", name: "Dave", room: scActiveRoom, joined: sat("10:00:03"),
		permission: scStandardPermission, client: &livekit.ClientInfo{Sdk: livekit.ClientInfo_ANDROID, Version: "2.9.0",
			Os: "Android", OsVersion: "15", DeviceModel: "Pixel 9", Network: "cellular", Address: "203.0.113.200"}}
	erin := scPerson{sid: scErin, identity: "erin", name: "Erin", room: scActiveRoom, joined: sat("10:31:00"),
		permission: scStandardPermission, client: &livekit.ClientInfo{Sdk: livekit.ClientInfo_JS, Version: "2.15.7",
			Os: "macOS", OsVersion: "15.1", Browser: "Firefox", BrowserVersion: "131.0", Network: "wifi",
			Address: "198.51.100.99"}}
	ingress1 := scPerson{sid: scIngress1, identity: "obs-studio", name: "OBS", kind: livekit.ParticipantInfo_INGRESS,
		room: scActiveRoom, joined: sat("10:00:10"), permission: scIngressPermission, client: scGoClient}
	ingress2 := scPerson{sid: scIngress2, identity: "obs-studio", name: "OBS", kind: livekit.ParticipantInfo_INGRESS,
		room: scActiveRoom, joined: sat("10:30:00"), permission: scIngressPermission, client: scGoClient}
	streamer := scPerson{sid: scStreamer, identity: scStreaming, kind: livekit.ParticipantInfo_EGRESS, room: scActiveRoom,
		joined: sat("10:05:00"), permission: scRecorderPermission, client: scGoClient}

	aliceMic := &livekit.TrackInfo{Sid: scAliceMic, Type: livekit.TrackType_AUDIO, Name: "microphone",
		Source: livekit.TrackSource_MICROPHONE, MimeType: "audio/opus", Codecs: []*livekit.SimulcastCodecInfo{{MimeType: "audio/opus"}}}
	daveCamera := &livekit.TrackInfo{Sid: scDaveCam, Type: livekit.TrackType_VIDEO, Name: "camera", Width: 640,
		Height: 480, Source: livekit.TrackSource_CAMERA, MimeType: "video/H264",
		Codecs: []*livekit.SimulcastCodecInfo{{MimeType: "video/H264", Layers: []*livekit.VideoLayer{
			{Quality: livekit.VideoQuality_HIGH, Width: 640, Height: 480, Bitrate: 800_000}}}}}
	daveMic := &livekit.TrackInfo{Sid: scDaveMic, Type: livekit.TrackType_AUDIO, Name: "microphone",
		Source: livekit.TrackSource_MICROPHONE, MimeType: "audio/opus", Codecs: []*livekit.SimulcastCodecInfo{{MimeType: "audio/opus"}}}
	ingressVideo := func(sid string) *livekit.TrackInfo {
		return &livekit.TrackInfo{Sid: sid, Type: livekit.TrackType_VIDEO, Width: 1920, Height: 1080,
			Source: livekit.TrackSource_CAMERA, MimeType: "video/H264",
			Codecs: []*livekit.SimulcastCodecInfo{{MimeType: "video/H264", Layers: []*livekit.VideoLayer{
				{Quality: livekit.VideoQuality_HIGH, Width: 1920, Height: 1080, Bitrate: 4_500_000}}}}}
	}
	publisherOf := func(p scPerson) *livekit.ParticipantInfo {
		return &livekit.ParticipantInfo{Sid: p.sid, Identity: p.identity}
	}
	sidOnly := func(p scPerson) *livekit.ParticipantInfo { return &livekit.ParticipantInfo{Sid: p.sid} }

	trackFailedRequest := &livekit.EgressInfo_Track{Track: &livekit.TrackEgressRequest{RoomName: scClosedName,
		TrackId: scAliceMic, Output: &livekit.TrackEgressRequest_File{File: &livekit.DirectFileOutput{
			Filepath: "tracks/alice-microphone.ogg"}}}}
	streamURL := "rtmps://user:pw@live.example.com/app/live_SECRET?token=abc"
	streamingEgress := func(status livekit.EgressStatus, updated time.Time) *livekit.EgressInfo {
		info := &livekit.EgressInfo{EgressId: scStreaming, RoomId: scActiveRoom, RoomName: scActiveName, Status: status,
			StartedAt: sat("10:05:00").UnixNano(), UpdatedAt: updated.UnixNano(),
			Request: &livekit.EgressInfo_RoomComposite{RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName: scActiveName, Layout: "grid",
				Options:        &livekit.RoomCompositeEgressRequest_Preset{Preset: livekit.EncodingOptionsPreset_H264_1080P_30},
				StreamOutputs:  []*livekit.StreamOutput{{Protocol: livekit.StreamProtocol_RTMP, Urls: []string{streamURL}}},
				SegmentOutputs: []*livekit.SegmentedFileOutput{{PlaylistName: "webinar-q4/playlist.m3u8"}},
			}}}
		if status == livekit.EgressStatus_EGRESS_ACTIVE {
			info.StreamResults = []*livekit.StreamInfo{{Url: streamURL, StartedAt: updated.UnixNano(),
				Status: livekit.StreamInfo_ACTIVE}}
			info.SegmentResults = []*livekit.SegmentsInfo{{PlaylistName: "webinar-q4/playlist.m3u8",
				PlaylistLocation: "https://storage.example.com/webinar-q4/playlist.m3u8", StartedAt: updated.UnixNano()}}
		}
		return info
	}
	enabled, transcoding := true, true
	rtmpIngress := func(state *livekit.IngressState) *livekit.IngressInfo {
		return &livekit.IngressInfo{IngressId: scRTMP, Name: "OBS Studio", StreamKey: "SECRET-streamkey",
			Url: "rtmp://ingress.example.com/x", RoomName: scActiveName, ParticipantIdentity: "obs-studio",
			ParticipantName: "OBS", Reusable: true, Enabled: &enabled, EnableTranscoding: &transcoding, State: state}
	}
	ingressVideoState := &livekit.InputVideoState{MimeType: "video/h264", AverageBitrate: 4_500_000, Width: 1920,
		Height: 1080, Framerate: 30}
	ingressAudioState := &livekit.InputAudioState{MimeType: "audio/opus", AverageBitrate: 128_000, Channels: 2,
		SampleRate: 48_000}

	return []*livekit.AnalyticsEvent{
		// Closed room on node alpha.
		scAPICall("AE_apiCreateRm01", scNodeAlpha, "livekit.RoomService", "CreateRoom", 200, scClosedName, sat("09:00:00")),
		scTrackEvent("AE_trPubMic00001", livekit.AnalyticsEventType_TRACK_PUBLISHED, scClosedRoom, publisherOf(alice), aliceMic, sat("09:00:08")),
		agent.joinedEvent("AE_paJoinAgent01"),
		agent.activeEvent("AE_paActAgent01", sat("09:00:11"), 900, "udp"),
		bob1.joinedEvent("AE_paJoinBob1001"),
		bob1.activeEvent("AE_paActBob10001", sat("09:01:01.500"), 1500, "udp"),
		scAPICall("AE_apiStartRc001", scNodeAlpha, "livekit.Egress", "StartRoomCompositeEgress", 200, scClosedName, sat("09:02:00")),
		recorder.joinedEvent("AE_paJoinRec0001"),
		recorder.activeEvent("AE_paActRec00001", sat("09:02:02"), 2000, "udp"),
		carol.joinedEvent("AE_paJoinCarol01"),
		scAPICall("AE_apiListPa0001", scNodeAlpha, "livekit.RoomService", "ListParticipants", 200, scClosedName, sat("09:06:00")),
		scAPICall("AE_apiListPa0002", scNodeAlpha, "livekit.RoomService", "ListParticipants", 404, "daily-standup", sat("09:07:00")),
		bob1.leftEvent("AE_paLeftBob1001", sat("09:10:00"), livekit.DisconnectReason_DUPLICATE_IDENTITY, false),
		bob2.joinedEvent("AE_paJoinBob2001"),
		bob2.activeEvent("AE_paActBob20001", sat("09:10:03"), 1000, "tcp"),
		scSubscription("AE_trSubBob20001", livekit.AnalyticsEventType_TRACK_SUBSCRIBED, scClosedRoom, scBob2, aliceCameraFull(), publisherOf(alice), sat("09:10:05")),
		scTrackEvent("AE_trMuteAlice01", livekit.AnalyticsEventType_TRACK_MUTED, scClosedRoom, sidOnly(alice),
			&livekit.TrackInfo{Sid: scAliceMic, Name: "microphone", Muted: true, Source: livekit.TrackSource_MICROPHONE, MimeType: "audio/opus"}, sat("09:12:00")),
		sip.joinedEvent("AE_paJoinSip0001"),
		sip.activeEvent("AE_paActSip00001", sat("09:20:02"), 2000, "udp"),
		sip.leftEvent("AE_paLeftSip0001", sat("09:30:00"), livekit.DisconnectReason_CLIENT_INITIATED, false),
		scAPICall("AE_apiStartTr001", scNodeAlpha, "livekit.Egress", "StartTrackEgress", 200, scClosedName, sat("09:30:00")),
		scEgress("AE_egStartFail01", livekit.AnalyticsEventType_EGRESS_STARTED, &livekit.EgressInfo{EgressId: scFailed,
			RoomId: scClosedRoom, RoomName: scClosedName, SourceType: livekit.EgressSourceType_EGRESS_SOURCE_TYPE_SDK, StartedAt: sat("09:30:00").UnixNano(),
			UpdatedAt: sat("09:30:00").UnixNano(), Request: trackFailedRequest}, sat("09:30:00")),
		scEgress("AE_egEndFail0001", livekit.AnalyticsEventType_EGRESS_ENDED, &livekit.EgressInfo{EgressId: scFailed,
			RoomId: scClosedRoom, RoomName: scClosedName, SourceType: livekit.EgressSourceType_EGRESS_SOURCE_TYPE_SDK,
			Status:    livekit.EgressStatus_EGRESS_FAILED,
			StartedAt: sat("09:30:00").UnixNano(), EndedAt: sat("09:30:40").UnixNano(), UpdatedAt: sat("09:30:40").UnixNano(),
			Error: "upload failed: access denied", ErrorCode: 403, RetryCount: 1, Request: trackFailedRequest,
			FileResults: []*livekit.FileInfo{{Filename: "tracks/alice-microphone.ogg", StartedAt: sat("09:30:00").UnixNano(),
				EndedAt: sat("09:30:40").UnixNano()}}}, sat("09:30:40")),
		scTrackEvent("AE_trUnpubMic001", livekit.AnalyticsEventType_TRACK_UNPUBLISHED, scClosedRoom, sidOnly(alice), aliceMic, sat("09:40:00")),
		scStatsEvent("AE_trPubStats002", livekit.AnalyticsEventType_TRACK_PUBLISH_STATS, scClosedRoom, scAlice, scAliceCam, "video/VP8", 0,
			scRTPStats(sat("09:00:08"), sat("09:40:00"), 52_000, 45_000_000, 30, 35_880, 0), sat("09:40:00")),
		scStatsEvent("AE_trPubStats003", livekit.AnalyticsEventType_TRACK_PUBLISH_STATS, scClosedRoom, scAlice, scAliceCam, "video/VP8", 1,
			scRTPStats(sat("09:00:08"), sat("09:40:00"), 98_000, 100_000_000, 40, 71_760, 0), sat("09:40:00")),
		scStatsEvent("AE_trPubStats004", livekit.AnalyticsEventType_TRACK_PUBLISH_STATS, scClosedRoom, scAlice, scAliceMic, "audio/opus", 0,
			scRTPStats(sat("09:00:08"), sat("09:40:00"), 119_600, 9_600_000, 30, 0, 2), sat("09:40:00")),
		scSubscription("AE_trUnsubBob201", livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED, scClosedRoom, scBob2,
			&livekit.TrackInfo{Sid: scAliceCam, Type: livekit.TrackType_VIDEO}, nil, sat("09:40:00")),
		scStatsEvent("AE_trSubStats002", livekit.AnalyticsEventType_TRACK_SUBSCRIBE_STATS, scClosedRoom, scBob2, scAliceCam, "video/VP8", 0,
			scRTPStats(sat("09:10:05"), sat("09:40:00"), 170_000, 180_000_000, 900, 0, 2), sat("09:40:00")),
		recorder.leftEvent("AE_paLeftRec0001", sat("09:40:05"), livekit.DisconnectReason_CLIENT_INITIATED, false),
		scEgress("AE_egEnding00001", livekit.AnalyticsEventType_EGRESS_UPDATED, recordingEgress(livekit.EgressStatus_EGRESS_ENDING, sat("09:40:06")), sat("09:40:06")),
		scAPICall("AE_apiDeleteRm01", scNodeAlpha, "livekit.RoomService", "DeleteRoom", 200, scClosedName, sat("09:45:00")),
		bob2.leftEvent("AE_paLeftBob2001", sat("09:45:00"), livekit.DisconnectReason_ROOM_DELETED, false),

		// Ingress and active room on node beta.
		scAPICall("AE_apiCreateIn01", scNodeBeta, "livekit.Ingress", "CreateIngress", 200, scActiveName, sat("09:50:00")),
		scIngress("AE_inCreatedWhip", livekit.AnalyticsEventType_INGRESS_CREATED, &livekit.IngressInfo{IngressId: scWHIP,
			Name: "Browser WHIP", StreamKey: "SECRET-whipkey", Url: "https://ingress.example.com/w",
			InputType: livekit.IngressInput_WHIP_INPUT, RoomName: scActiveName, ParticipantIdentity: "whip-publisher",
			Enabled: &enabled, State: &livekit.IngressState{}}, sat("09:50:00")),
		scAPICall("AE_apiCreateIn02", scNodeBeta, "livekit.Ingress", "CreateIngress", 200, scActiveName, sat("09:55:00")),
		stamp(&livekit.AnalyticsEvent{Room: &livekit.Room{Sid: scActiveRoom, Name: scActiveName, EmptyTimeout: 300,
			DepartureTimeout: 20, CreationTime: sat("10:00:00").Unix(), CreationTimeMs: sat("10:00:00").UnixMilli()}},
			"AE_rmCreated0002", livekit.AnalyticsEventType_ROOM_CREATED, scNodeBeta, sat("10:00:00")),
		dave.joinedEvent("AE_paJoinDave001"),
		dave.activeEvent("AE_paActDave0001", sat("10:00:04"), 900, "turn"),
		scTrackEvent("AE_trPubDaveCam1", livekit.AnalyticsEventType_TRACK_PUBLISHED, scActiveRoom, publisherOf(dave), daveCamera, sat("10:00:06")),
		scTrackEvent("AE_trPubDaveMic1", livekit.AnalyticsEventType_TRACK_PUBLISHED, scActiveRoom, publisherOf(dave), daveMic, sat("10:00:06")),
		scIngress("AE_inStartedSes1", livekit.AnalyticsEventType_INGRESS_STARTED, rtmpIngress(&livekit.IngressState{
			Status: livekit.IngressState_ENDPOINT_PUBLISHING, Video: ingressVideoState, Audio: ingressAudioState,
			RoomId: scActiveRoom, StartedAt: sat("10:00:10").UnixNano(), UpdatedAt: sat("10:00:10").UnixNano(),
			ResourceId: scSession1}), sat("10:00:10")),
		ingress1.joinedEvent("AE_paJoinIng1001"),
		ingress1.activeEvent("AE_paActIng10001", sat("10:00:11"), 500, "udp"),
		scTrackEvent("AE_trPubIngVid01", livekit.AnalyticsEventType_TRACK_PUBLISHED, scActiveRoom, publisherOf(ingress1), ingressVideo(scIngVideo1), sat("10:00:12")),
		scSubscription("AE_trSubDave0001", livekit.AnalyticsEventType_TRACK_SUBSCRIBED, scActiveRoom, scDave, ingressVideo(scIngVideo1), publisherOf(ingress1), sat("10:00:15")),
		scAPICall("AE_apiStartRc002", scNodeBeta, "livekit.Egress", "StartRoomCompositeEgress", 200, scActiveName, sat("10:05:00")),
		scEgress("AE_egStartStrm01", livekit.AnalyticsEventType_EGRESS_STARTED, streamingEgress(livekit.EgressStatus_EGRESS_STARTING, sat("10:05:00")), sat("10:05:00")),
		streamer.joinedEvent("AE_paJoinStrm001"),
		streamer.activeEvent("AE_paActStrm0001", sat("10:05:02"), 1800, "udp"),
		scEgress("AE_egActiveStrm1", livekit.AnalyticsEventType_EGRESS_UPDATED, streamingEgress(livekit.EgressStatus_EGRESS_ACTIVE, sat("10:05:03")), sat("10:05:03")),
		scSubscription("AE_trUnsubDave01", livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED, scActiveRoom, scDave,
			&livekit.TrackInfo{Sid: scIngVideo1, Type: livekit.TrackType_VIDEO}, nil, sat("10:20:00")),
		scStatsEvent("AE_trSubStats003", livekit.AnalyticsEventType_TRACK_SUBSCRIBE_STATS, scActiveRoom, scDave, scIngVideo1, "video/H264", 0,
			scRTPStats(sat("10:00:15"), sat("10:20:00"), 470_000, 660_000_000, 15, 0, 2), sat("10:20:00")),
		scTrackEvent("AE_trUnpubIng101", livekit.AnalyticsEventType_TRACK_UNPUBLISHED, scActiveRoom, sidOnly(ingress1), ingressVideo(scIngVideo1), sat("10:20:00")),
		scStatsEvent("AE_trPubStats005", livekit.AnalyticsEventType_TRACK_PUBLISH_STATS, scActiveRoom, scIngress1, scIngVideo1, "video/H264", 0,
			scRTPStats(sat("10:00:12"), sat("10:20:00"), 480_000, 668_000_000, 40, 35_640, 2), sat("10:20:00")),
		ingress1.leftEvent("AE_paLeftIng1001", sat("10:20:00"), livekit.DisconnectReason_CLIENT_INITIATED, true),
		ingress2.joinedEvent("AE_paJoinIng2001"),
		ingress2.activeEvent("AE_paActIng20001", sat("10:30:01"), 600, "udp"),
		scTrackEvent("AE_trPubIngVid02", livekit.AnalyticsEventType_TRACK_PUBLISHED, scActiveRoom, publisherOf(ingress2), ingressVideo(scIngVideo2), sat("10:30:02")),
		scSubscription("AE_trSubDave0002", livekit.AnalyticsEventType_TRACK_SUBSCRIBED, scActiveRoom, scDave, ingressVideo(scIngVideo2), publisherOf(ingress2), sat("10:30:05")),
		erin.joinedEvent("AE_paJoinErin001"),
		scSubscription("AE_trSubReqErin1", livekit.AnalyticsEventType_TRACK_SUBSCRIBE_REQUESTED, scActiveRoom, scErin,
			&livekit.TrackInfo{Sid: scDaveCam}, nil, sat("10:31:00")),
	}
}

func aliceCameraFull() *livekit.TrackInfo {
	return &livekit.TrackInfo{Sid: scAliceCam, Type: livekit.TrackType_VIDEO, Name: "camera", Width: 1280, Height: 720,
		Simulcast: true, Source: livekit.TrackSource_CAMERA, MimeType: "video/VP8", Stream: "camera",
		Layers: []*livekit.VideoLayer{{Width: 320, Height: 180, Bitrate: 150_000},
			{Quality: livekit.VideoQuality_MEDIUM, Width: 640, Height: 360, Bitrate: 500_000},
			{Quality: livekit.VideoQuality_HIGH, Width: 1280, Height: 720, Bitrate: 1_700_000}},
		Codecs: []*livekit.SimulcastCodecInfo{{MimeType: "video/VP8", Mid: "1", Cid: "camera-vp8"}}}
}

func recordingEgress(status livekit.EgressStatus, updated time.Time) *livekit.EgressInfo {
	return &livekit.EgressInfo{EgressId: scRecording, RoomId: scClosedRoom, RoomName: scClosedName, Status: status,
		StartedAt: sat("09:02:00").UnixNano(), UpdatedAt: updated.UnixNano(),
		Request: &livekit.EgressInfo_RoomComposite{RoomComposite: &livekit.RoomCompositeEgressRequest{
			RoomName: scClosedName, Layout: "speaker",
			Options:     &livekit.RoomCompositeEgressRequest_Preset{Preset: livekit.EncodingOptionsPreset_H264_720P_30},
			FileOutputs: []*livekit.EncodedFileOutput{{FileType: livekit.EncodedFileType_MP4, Filepath: "recordings/daily-standup-2026-10-12.mp4"}},
		}}}
}

// --- Stats --------------------------------------------------------------------------------------------------------

// statsPlan describes the 30-second stats of a participant in participant detail: one media sample per direction
// and flush from first to last; scoreCount of them carry a score adding up to scoreSum, with the lowest score min,
// the scores below the poor band given in poor and lastScore at the last flush.
type statsPlan struct {
	room, participant  string
	first, last        time.Time
	up, down           uint64
	scoreSum           float64
	scoreCount         int
	min, lastScore     float64
	poor               []float64
	fixedUp, fixedDown []*livekit.AnalyticsStat
}

func contractStats() []statsFlush {
	plans := []statsPlan{
		{room: scClosedRoom, participant: scAlice, first: sat("09:00:30"), last: sat("09:39:30"), up: 380_000_000, down: 2_000_000,
			scoreSum: 600, scoreCount: 150, min: 3.9, lastScore: 4.1},
		{room: scClosedRoom, participant: scBob1, first: sat("09:01:30"), last: sat("09:09:30"), up: 1_000_000, down: 120_000_000,
			scoreSum: 72, scoreCount: 18, min: 3.8, lastScore: 4},
		{room: scClosedRoom, participant: scBob2, first: sat("09:10:30"), last: sat("09:44:30"), up: 3_000_000, down: 480_000_000,
			scoreSum: 264, scoreCount: 70, min: 1.8, lastScore: 3.9, poor: []float64{1.8, 2, 2, 2, 2, 2}},
		{room: scClosedRoom, participant: scRecorder, first: sat("09:02:30"), last: sat("09:39:30"), up: 500_000, down: 250_000_000,
			scoreSum: 160, scoreCount: 40, min: 3.7, lastScore: 4},
		{room: scClosedRoom, participant: scAgent, first: sat("09:00:30"), last: sat("09:44:30"), up: 400_000, down: 30_000_000,
			scoreSum: 120, scoreCount: 30, min: 3.9, lastScore: 4},
		{room: scClosedRoom, participant: scSIP, first: sat("09:20:30"), last: sat("09:29:30"), up: 100_000, down: 6_000_000,
			scoreSum: 51, scoreCount: 12, min: 4.2, lastScore: 4.3},
		{room: scActiveRoom, participant: scDave, first: sat("10:00:30"), last: sat("10:35:00"), up: 90_000_000, down: 58_000_000,
			scoreSum: 420, scoreCount: 104, min: 3.6, lastScore: 4.1, fixedUp: daveLastFlushUp(), fixedDown: daveLastFlushDown()},
		{room: scActiveRoom, participant: scIngress1, first: sat("10:00:30"), last: sat("10:19:30"), up: 7_000_000, down: 100_000,
			scoreSum: 40, scoreCount: 10, min: 3.9, lastScore: 4},
		{room: scActiveRoom, participant: scIngress2, first: sat("10:30:30"), last: sat("10:35:00"), up: 1_500_000, down: 20_000,
			scoreSum: 26, scoreCount: 6, min: 4.2, lastScore: 4.4},
		{room: scActiveRoom, participant: scStreamer, first: sat("10:05:30"), last: sat("10:35:00"), up: 300_000, down: 41_000_000},
	}

	flushes := map[time.Time][]*livekit.AnalyticsStat{}
	for _, plan := range plans {
		for at, stats := range plan.samples() {
			flushes[at] = append(flushes[at], stats...)
		}
	}
	var ordered []statsFlush
	for at, stats := range flushes {
		// Downstream first, so that the upstream score of a flush is the participant's last score.
		sort.SliceStable(stats, func(i, j int) bool { return stats[i].GetKind() > stats[j].GetKind() })
		ordered = append(ordered, statsFlush{at: at, stats: stats})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].at.Before(ordered[j].at) })
	return ordered
}

func (plan statsPlan) samples() map[time.Time][]*livekit.AnalyticsStat {
	var instants []time.Time
	for at := plan.first; !at.After(plan.last); at = at.Add(30 * time.Second) {
		instants = append(instants, at)
	}
	fixedAt := plan.last
	hasFixed := len(plan.fixedUp) > 0

	// Scores: the fixed flush (if any) and the last upstream sample are set; the others take min, the poor ones
	// and fillers that make up scoreSum, from the earliest samples on.
	type slot struct {
		at   time.Time
		kind livekit.StreamType
	}
	var slots []slot
	for _, at := range instants {
		if hasFixed && at.Equal(fixedAt) {
			continue
		}
		for _, kind := range []livekit.StreamType{livekit.StreamType_UPSTREAM, livekit.StreamType_DOWNSTREAM} {
			if !hasFixed && at.Equal(plan.last) {
				if kind == livekit.StreamType_UPSTREAM {
					continue
				}
			}
			slots = append(slots, slot{at, kind})
		}
	}

	fixedSum, fixedCount := 0.0, 0
	for _, stats := range [][]*livekit.AnalyticsStat{plan.fixedUp, plan.fixedDown} {
		if len(stats) > 0 {
			fixedSum += mergedScore(stats)
			fixedCount++
		}
	}
	scores := map[slot]float64{}
	if plan.scoreCount > 0 {
		var values []float64
		values = append(values, plan.poor...)
		if len(plan.poor) == 0 || plan.poor[0] != plan.min {
			values = append(values, plan.min)
		}
		lastCount := 0
		if !hasFixed {
			lastCount = 1
			fixedSum += plan.lastScore
		}
		fillers := plan.scoreCount - fixedCount - lastCount - len(values)
		remaining := plan.scoreSum - fixedSum
		for _, v := range values {
			remaining -= v
		}
		cents := int(math.Round(remaining * 100))
		base := cents / fillers
		extra := cents - base*fillers
		for i := range fillers {
			value := base
			if i < extra {
				value++
			}
			values = append(values, float64(value)/100)
		}
		for i, value := range values {
			scores[slots[i]] = value
		}
	}

	upSlots, downSlots := 0, 0
	for _, s := range slots {
		if s.kind == livekit.StreamType_UPSTREAM {
			upSlots++
		} else {
			downSlots++
		}
	}
	up, down := plan.up, plan.down
	for _, stats := range plan.fixedUp {
		up -= uint64(sampleBytes(stats))
	}
	for _, stats := range plan.fixedDown {
		down -= uint64(sampleBytes(stats))
	}
	if !hasFixed {
		upSlots++
	}

	out := map[time.Time][]*livekit.AnalyticsStat{}
	share := func(total uint64, parts int, index int) uint64 {
		value := total / uint64(parts)
		if index == 0 {
			value += total % uint64(parts)
		}
		return value
	}
	upIndex, downIndex := 0, 0
	for _, s := range slots {
		var bytes uint64
		if s.kind == livekit.StreamType_UPSTREAM {
			bytes = share(up, upSlots, upIndex)
			upIndex++
		} else {
			bytes = share(down, downSlots, downIndex)
			downIndex++
		}
		out[s.at] = append(out[s.at], plan.stat(s.kind, s.at, scores[s], bytes))
	}
	if hasFixed {
		out[fixedAt] = append(out[fixedAt], plan.fixedDown...)
		out[fixedAt] = append(out[fixedAt], plan.fixedUp...)
	} else {
		out[plan.last] = append(out[plan.last], plan.stat(livekit.StreamType_UPSTREAM, plan.last, plan.lastScore, share(up, upSlots, upIndex)))
	}
	return out
}

func (plan statsPlan) stat(kind livekit.StreamType, at time.Time, score float64, bytes uint64) *livekit.AnalyticsStat {
	track := "TR_up" + plan.participant[3:]
	if kind == livekit.StreamType_DOWNSTREAM {
		track = "TR_dn" + plan.participant[3:]
	}
	return &livekit.AnalyticsStat{
		Id: fmt.Sprintf("AS_%s_%d_%d", plan.participant, kind, at.Unix()), Kind: kind, TimeStamp: timestamppb.New(at),
		Node: scNode(plan.room), RoomId: plan.room, ParticipantId: plan.participant, TrackId: track,
		Score: float32(score), MinScore: float32(score), MedianScore: float32(score),
		Streams: []*livekit.AnalyticsStream{{PrimaryBytes: bytes, PrimaryPackets: uint32(bytes / 1000), Rtt: 40, Jitter: 2500}},
	}
}

func sampleBytes(stat *livekit.AnalyticsStat) int64 {
	total := int64(0)
	for _, stream := range stat.GetStreams() {
		total += int64(stream.GetPrimaryBytes())
	}
	return total
}

// mergedScore is the score of the participant document a group of stats merges into.
func mergedScore(stats []*livekit.AnalyticsStat) float64 {
	total := 0.0
	for _, stat := range stats {
		total += float64(stat.GetScore())
	}
	return math.Round(float64(float32(total/float64(len(stats))))*100) / 100
}

func scMediaStat(kind livekit.StreamType, participant string, track string, mime string, score float32, min float32,
	bytes uint64, packets uint32, lost uint32, rtt uint32, jitterUs uint32, nacks uint32, frames uint32, layered bool) *livekit.AnalyticsStat {
	stream := &livekit.AnalyticsStream{PrimaryBytes: bytes, PrimaryPackets: packets, PacketsLost: lost, Rtt: rtt,
		Jitter: jitterUs, Nacks: nacks, Frames: frames}
	if layered {
		stream.VideoLayers = []*livekit.AnalyticsVideoLayer{{Layer: 0, Bytes: bytes, Packets: packets, Frames: frames}}
	}
	at := sat("10:35:00")
	return &livekit.AnalyticsStat{
		Id: fmt.Sprintf("AS_%s_%s_%d", participant, track, at.Unix()), Kind: kind, TimeStamp: timestamppb.New(at),
		Node: scNodeBeta, RoomId: scActiveRoom, ParticipantId: participant, TrackId: track, Score: score, MinScore: min,
		MedianScore: score, Mime: mime, Streams: []*livekit.AnalyticsStream{stream},
	}
}

// daveLastFlushUp and daveLastFlushDown are the 10:35:00 flush of the track_stats fixtures.
func daveLastFlushUp() []*livekit.AnalyticsStat {
	return []*livekit.AnalyticsStat{
		scMediaStat(livekit.StreamType_UPSTREAM, scDave, scDaveCam, "video/H264", 4.2, 4, 3_000_000, 2_900, 2, 45, 3200, 1, 900, true),
		scMediaStat(livekit.StreamType_UPSTREAM, scDave, scDaveMic, "audio/opus", 4, 3.9, 150_000, 500, 0, 44, 1100, 0, 0, false),
	}
}

func daveLastFlushDown() []*livekit.AnalyticsStat {
	return []*livekit.AnalyticsStat{
		scMediaStat(livekit.StreamType_DOWNSTREAM, scDave, scIngVideo2, "video/H264", 4.3, 4.3, 16_875_000, 12_000, 0, 45, 2100, 0, 900, true),
	}
}

// --- Comparison ---------------------------------------------------------------------------------------------------

// compareCollection checks that every fixture document has a stored twin equal in every contract field (`internal`
// is writer bookkeeping and the daily `uniqueIdentities` is the reconciler's); exact also requires no other document.
func compareCollection(t *testing.T, db *mongo.Database, collection string, exact bool) {
	t.Helper()
	fixtures, err := schema.Fixtures(collection)
	require.NoError(t, err)
	stored := map[string]map[string]any{}
	cursor, err := db.Collection(collection).Find(context.Background(), bson.D{})
	require.NoError(t, err)
	var documents []bson.D
	require.NoError(t, cursor.All(context.Background(), &documents))
	for _, document := range documents {
		normalized := normalizeDocument(document)
		stored[fmt.Sprint(normalized["_id"])] = normalized
	}

	expectedIDs := map[string]bool{}
	for _, raw := range fixtures {
		var document bson.D
		require.NoError(t, bson.Unmarshal(raw, &document))
		expected := normalizeDocument(document)
		id := fmt.Sprint(expected["_id"])
		if collection == schema.CollectionSchemaMeta && (id == "migration" || id == "reconciler") {
			continue
		}
		expectedIDs[id] = true
		actual, ok := stored[id]
		if !check(t, ok, "%s %s not written", collection, id) {
			continue
		}
		for _, difference := range differences("", expected, actual) {
			t.Errorf("%s %s: %s", collection, id, difference)
		}
	}
	if exact {
		for id := range stored {
			check(t, expectedIDs[id], "%s %s written but not in the fixtures", collection, id)
		}
	}
}

func compareTrackStats(t *testing.T, db *mongo.Database, detail string) {
	t.Helper()
	fixtures, err := schema.Fixtures(schema.CollectionTrackStats)
	require.NoError(t, err)
	cursor, err := db.Collection(schema.CollectionTrackStats).Find(context.Background(), bson.D{})
	require.NoError(t, err)
	var documents []bson.D
	require.NoError(t, cursor.All(context.Background(), &documents))
	stored := map[string]map[string]any{}
	key := func(document map[string]any) string {
		return fmt.Sprint(document["ts"], document["meta"])
	}
	for _, document := range documents {
		normalized := normalizeDocument(document)
		delete(normalized, "_id")
		stored[key(normalized)] = normalized
	}
	for _, raw := range fixtures {
		var document bson.D
		require.NoError(t, bson.Unmarshal(raw, &document))
		expected := normalizeDocument(document)
		_, perTrack := expected["meta"].(map[string]any)["trackSid"]
		if perTrack != (detail == "track") {
			continue
		}
		actual, ok := stored[key(expected)]
		if !check(t, ok, "track_stats %v not written", expected["meta"]) {
			continue
		}
		for _, difference := range differences("", expected, actual) {
			t.Errorf("track_stats %v: %s", expected["meta"], difference)
		}
	}
}

func check(t *testing.T, ok bool, format string, args ...any) bool {
	t.Helper()
	if !ok {
		t.Errorf(format, args...)
	}
	return ok
}

// normalizeDocument turns a document into maps and slices, without the writer's internal bookkeeping.
func normalizeDocument(document bson.D) map[string]any {
	out := normalize(document).(map[string]any)
	delete(out, "internal")
	return out
}

func normalize(value any) any {
	switch v := value.(type) {
	case bson.D:
		out := map[string]any{}
		for _, element := range v {
			out[element.Key] = normalize(element.Value)
		}
		return out
	case bson.A:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalize(item)
		}
		return out
	case bson.DateTime:
		return v.Time().UTC()
	default:
		return v
	}
}

// differences lists the paths where actual differs from expected, BSON types included; doubles compare within 1e-6.
func differences(path string, expected any, actual any) []string {
	switch e := expected.(type) {
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: expected a document, got %T %v", path, actual, actual)}
		}
		var out []string
		for key, value := range e {
			child := strings.TrimPrefix(path+"."+key, ".")
			if _, present := a[key]; !present {
				out = append(out, fmt.Sprintf("%s: missing, expected %T %v", child, value, value))
				continue
			}
			out = append(out, differences(child, value, a[key])...)
		}
		for key, value := range a {
			if _, present := e[key]; !present {
				out = append(out, fmt.Sprintf("%s: unexpected %T %v", strings.TrimPrefix(path+"."+key, "."), value, value))
			}
		}
		sort.Strings(out)
		return out
	case []any:
		a, ok := actual.([]any)
		if !ok || len(a) != len(e) {
			return []string{fmt.Sprintf("%s: expected %v, got %v", path, e, actual)}
		}
		var out []string
		for i := range e {
			out = append(out, differences(fmt.Sprintf("%s[%d]", path, i), e[i], a[i])...)
		}
		return out
	case float64:
		a, ok := actual.(float64)
		if !ok || math.Abs(a-e) > 1e-6 {
			return []string{fmt.Sprintf("%s: expected double %v, got %T %v", path, e, actual, actual)}
		}
		return nil
	case time.Time:
		a, ok := actual.(time.Time)
		if !ok || !a.Equal(e) {
			return []string{fmt.Sprintf("%s: expected date %s, got %v", path, e.Format(time.RFC3339Nano), actual)}
		}
		return nil
	default:
		if fmt.Sprintf("%T", expected) != fmt.Sprintf("%T", actual) || expected != actual {
			return []string{fmt.Sprintf("%s: expected %T %v, got %T %v", path, expected, expected, actual, actual)}
		}
		return nil
	}
}
