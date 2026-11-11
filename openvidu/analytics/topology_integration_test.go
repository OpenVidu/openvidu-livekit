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
	"fmt"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/testutil"
)

// mutedMicrophone is a microphone published muted, as OpenVidu Meet publishes a device turned off before joining.
func mutedMicrophone(sid string) *livekit.TrackInfo {
	track := microphoneTrack(sid)
	track.Muted = true
	return track
}

// roomWith returns the events of a room in which every participant joins and becomes active at offset, and each
// sender publishes an unmuted microphone right after.
func roomWith(ev *events, room string, participants []*livekit.ParticipantInfo, senders map[string]bool,
	offset string) []*livekit.AnalyticsEvent {
	batch := []*livekit.AnalyticsEvent{ev.roomCreated(room, "room-"+room, at("0s"))}
	for _, p := range participants {
		batch = append(batch, ev.joined(room, p, at(offset)), ev.active(room, p, at(offset)))
		if senders[p.GetSid()] {
			batch = append(batch, ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, room, p,
				microphoneTrack("TR_"+p.GetSid()), at(offset)))
		}
	}
	return batch
}

// standard returns n STANDARD participants of room, the first n of them senders when senders > 0.
func standard(room string, n int, senders int) ([]*livekit.ParticipantInfo, map[string]bool) {
	participants := make([]*livekit.ParticipantInfo, n)
	sending := map[string]bool{}
	for i := range participants {
		sid := fmt.Sprintf("PA_%s_%d", room, i)
		participants[i] = participantInfo(sid, fmt.Sprintf("user-%d", i), livekit.ParticipantInfo_STANDARD, at("1s"))
		if i < senders {
			sending[sid] = true
		}
	}
	return participants, sending
}

func TestRoomTopology(t *testing.T) {
	client := testutil.StartMongo(t)

	t.Run("meeting with one speaker", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		participants, _ := standard("RM_1", 5, 0)
		batch := roomWith(&ev, "RM_1", participants, nil, "1s")
		for _, p := range participants {
			batch = append(batch, ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_1", p,
				mutedMicrophone("TR_"+p.GetSid()), at("2s")))
		}
		speaker := participants[0]
		for i := range 3 {
			batch = append(batch,
				ev.track(livekit.AnalyticsEventType_TRACK_UNMUTED, "RM_1", speaker, microphoneTrack("TR_"+speaker.GetSid()),
					at(fmt.Sprintf("%dm", 2*i+1))),
				ev.track(livekit.AnalyticsEventType_TRACK_MUTED, "RM_1", speaker, mutedMicrophone("TR_"+speaker.GetSid()),
					at(fmt.Sprintf("%dm", 2*i+2))))
		}
		env.apply(batch)

		room := find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1")
		require.EqualValues(t, 5, room.Participants.PeakActive)
		require.EqualValues(t, 1, *room.Participants.PeakPublishers)
		require.Equal(t, "one_to_many", room.Topology)
	})

	t.Run("participants taking turns", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		participants, _ := standard("RM_1", 4, 0)
		batch := roomWith(&ev, "RM_1", participants, nil, "1s")
		for _, p := range participants {
			batch = append(batch, ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_1", p,
				mutedMicrophone("TR_"+p.GetSid()), at("2s")))
		}
		env.apply(batch)
		for i, p := range participants[:2] {
			env.apply([]*livekit.AnalyticsEvent{
				ev.track(livekit.AnalyticsEventType_TRACK_UNMUTED, "RM_1", p, microphoneTrack("TR_"+p.GetSid()),
					at(fmt.Sprintf("%dm", 2*i+1))),
				ev.track(livekit.AnalyticsEventType_TRACK_MUTED, "RM_1", p, mutedMicrophone("TR_"+p.GetSid()),
					at(fmt.Sprintf("%dm", 2*i+2))),
			})
		}

		room := find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1")
		require.EqualValues(t, 2, *room.Participants.PeakPublishers)
		require.Equal(t, "many_to_many", room.Topology)
	})

	t.Run("senders one after the other", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		alice := participantInfo("PA_a", "alice", livekit.ParticipantInfo_STANDARD, at("1s"))
		bob := participantInfo("PA_b", "bob", livekit.ParticipantInfo_STANDARD, at("2m"))
		env.apply(append(roomWith(&ev, "RM_1", []*livekit.ParticipantInfo{alice}, map[string]bool{"PA_a": true}, "1s"),
			ev.left("RM_1", alice, at("1m"), livekit.DisconnectReason_CLIENT_INITIATED)))
		env.apply(roomWith(&ev, "RM_1", []*livekit.ParticipantInfo{bob}, map[string]bool{"PA_b": true}, "2m")[1:])

		room := find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1")
		require.EqualValues(t, 1, *room.Participants.PeakPublishers)
		require.Equal(t, "single_participant", room.Topology)
	})

	t.Run("peak inside one batch", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		participants, senders := standard("RM_1", 2, 2)
		env.apply(append(roomWith(&ev, "RM_1", participants, senders, "1s"),
			ev.left("RM_1", participants[0], at("1m"), livekit.DisconnectReason_CLIENT_INITIATED)))

		room := find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1")
		require.EqualValues(t, 1, room.Participants.Active)
		require.EqualValues(t, 2, *room.Participants.PeakPublishers)
		require.Equal(t, "one_to_one", room.Topology)
	})

	t.Run("track published muted and never unmuted", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		participants, _ := standard("RM_1", 3, 0)
		batch := roomWith(&ev, "RM_1", participants, nil, "1s")
		batch = append(batch, ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_1", participants[0],
			mutedMicrophone("TR_muted"), at("2s")))
		env.apply(batch)

		room := find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1")
		require.EqualValues(t, 0, *room.Participants.PeakPublishers)
		require.Equal(t, "no_publishers", room.Topology)
	})

	t.Run("agent speaking", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		alice := participantInfo("PA_a", "alice", livekit.ParticipantInfo_STANDARD, at("1s"))
		agent := participantInfo("PA_g", "agent-1", livekit.ParticipantInfo_AGENT, at("1s"))
		env.apply(roomWith(&ev, "RM_1", []*livekit.ParticipantInfo{alice, agent}, map[string]bool{"PA_g": true}, "1s"))

		room := find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1")
		require.EqualValues(t, 0, *room.Participants.PeakPublishers)
		require.Equal(t, "single_participant", room.Topology)
	})

	t.Run("classes from the peaks", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		cases := []struct {
			room       string
			n, senders int
			topology   string
		}{
			{"RM_empty", 0, 0, "empty"},
			{"RM_single", 1, 1, "single_participant"},
			{"RM_audience", 5, 0, "no_publishers"},
			{"RM_call", 2, 1, "one_to_one"},
			{"RM_talk", 2, 2, "one_to_one"},
			{"RM_webinar", 40, 1, "one_to_many"},
			{"RM_meeting", 6, 3, "many_to_many"},
		}
		var batch []*livekit.AnalyticsEvent
		for _, c := range cases {
			participants, senders := standard(c.room, c.n, c.senders)
			batch = append(batch, roomWith(&ev, c.room, participants, senders, "1s")...)
			batch = append(batch, ev.roomEnded(c.room, "room-"+c.room, at("0s"), at("10m"),
				livekit.RoomEndReason_ROOM_END_API_DELETE))
		}
		env.apply(batch)

		for _, c := range cases {
			room := find[schema.RoomDocument](env, schema.CollectionRooms, c.room)
			require.Equal(t, c.topology, room.Topology, c.room)
			require.EqualValues(t, c.n, room.Participants.PeakActive, c.room)
			require.EqualValues(t, c.senders, *room.Participants.PeakPublishers, c.room)
		}
	})

	t.Run("active room", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		participants, senders := standard("RM_1", 3, 1)
		env.apply(roomWith(&ev, "RM_1", participants[:2], senders, "1s"))
		require.Equal(t, "one_to_one", find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1").Topology)

		third := participants[2]
		env.apply([]*livekit.AnalyticsEvent{
			ev.joined("RM_1", third, at("1m")), ev.active("RM_1", third, at("1m")),
			ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_1", third, mutedMicrophone("TR_third"), at("1m")),
		})
		require.Equal(t, "one_to_many", find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1").Topology)

		env.apply([]*livekit.AnalyticsEvent{
			ev.track(livekit.AnalyticsEventType_TRACK_UNMUTED, "RM_1", third, microphoneTrack("TR_third"), at("2m")),
		})
		room := find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1")
		require.Equal(t, "many_to_many", room.Topology)
		require.Equal(t, "active", room.Status)
	})
}

// egressWith returns an egress of room whose request has the given outputs only.
func egressWith(id string, room string, status livekit.EgressStatus, request any) *livekit.EgressInfo {
	info := &livekit.EgressInfo{EgressId: id, RoomId: room, RoomName: "room-" + room, Status: status,
		StartedAt: at("10s").UnixNano(), UpdatedAt: at("10s").UnixNano()}
	switch r := request.(type) {
	case *livekit.RoomCompositeEgressRequest:
		info.Request = &livekit.EgressInfo_RoomComposite{RoomComposite: r}
	case *livekit.TrackCompositeEgressRequest:
		info.Request = &livekit.EgressInfo_TrackComposite{TrackComposite: r}
	case *livekit.ParticipantEgressRequest:
		info.Request = &livekit.EgressInfo_Participant{Participant: r}
	}
	return info
}

func TestRecordedRoom(t *testing.T) {
	client := testutil.StartMongo(t)
	recording := func(env *testEnv) *bool {
		env.t.Helper()
		return find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1").Features.Recording
	}

	t.Run("recording that completes", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		info := func(status livekit.EgressStatus) *livekit.EgressInfo {
			return roomCompositeEgress("EG_1", "RM_1", status, at("10s"), at("10s"))
		}
		env.apply([]*livekit.AnalyticsEvent{ev.roomCreated("RM_1", "room-RM_1", at("0s")),
			ev.egress(livekit.AnalyticsEventType_EGRESS_STARTED, info(livekit.EgressStatus_EGRESS_STARTING), at("10s"))})
		require.False(t, *recording(env))
		env.apply([]*livekit.AnalyticsEvent{
			ev.egress(livekit.AnalyticsEventType_EGRESS_UPDATED, info(livekit.EgressStatus_EGRESS_ACTIVE), at("12s"))})
		require.True(t, *recording(env))
		env.apply([]*livekit.AnalyticsEvent{
			ev.egress(livekit.AnalyticsEventType_EGRESS_ENDED, info(livekit.EgressStatus_EGRESS_COMPLETE), at("5m"))})
		require.True(t, *recording(env))
	})

	t.Run("recording that fails before starting", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		request := &livekit.TrackCompositeEgressRequest{RoomName: "room-RM_1", AudioTrackId: "TR_mic",
			SegmentOutputs: []*livekit.SegmentedFileOutput{{PlaylistName: "room/playlist.m3u8"}}}
		env.apply([]*livekit.AnalyticsEvent{ev.roomCreated("RM_1", "room-RM_1", at("0s")),
			ev.egress(livekit.AnalyticsEventType_EGRESS_STARTED,
				egressWith("EG_1", "RM_1", livekit.EgressStatus_EGRESS_STARTING, request), at("10s")),
			ev.egress(livekit.AnalyticsEventType_EGRESS_ENDED,
				egressWith("EG_1", "RM_1", livekit.EgressStatus_EGRESS_FAILED, request), at("20s")),
		})
		require.False(t, *recording(env))
	})

	t.Run("live stream", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		request := &livekit.RoomCompositeEgressRequest{RoomName: "room-RM_1",
			StreamOutputs: []*livekit.StreamOutput{{Urls: []string{"rtmp://live.example.com/app/key"}}}}
		env.apply([]*livekit.AnalyticsEvent{ev.roomCreated("RM_1", "room-RM_1", at("0s")),
			ev.egress(livekit.AnalyticsEventType_EGRESS_UPDATED,
				egressWith("EG_1", "RM_1", livekit.EgressStatus_EGRESS_ACTIVE, request), at("10s")),
		})
		room := find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1")
		require.True(t, room.Features.Egress)
		require.False(t, *room.Features.Recording)
	})

	t.Run("HLS of a participant", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		request := &livekit.ParticipantEgressRequest{RoomName: "room-RM_1", Identity: "alice",
			SegmentOutputs: []*livekit.SegmentedFileOutput{{PlaylistName: "alice/playlist.m3u8"}}}
		env.apply([]*livekit.AnalyticsEvent{ev.roomCreated("RM_1", "room-RM_1", at("0s")),
			ev.egress(livekit.AnalyticsEventType_EGRESS_UPDATED,
				egressWith("EG_1", "RM_1", livekit.EgressStatus_EGRESS_ACTIVE, request), at("10s")),
		})
		require.True(t, *recording(env))
	})
}

func TestRoomClassCounters(t *testing.T) {
	client := testutil.StartMongo(t)
	hourID := "2026-10-12T10:00:00Z"
	dayID := "2026-10-12T00:00:00Z"

	t.Run("ended rooms", func(t *testing.T) {
		env := newEnv(t, client)
		env.p.opts.MediaNode = testMediaNode
		var ev events
		meeting, meetingSenders := standard("RM_meeting", 12, 3)
		call, callSenders := standard("RM_call", 2, 2)
		batch := roomWith(&ev, "RM_meeting", meeting, meetingSenders, "1s")
		batch = append(batch, ev.egress(livekit.AnalyticsEventType_EGRESS_UPDATED,
			roomCompositeEgress("EG_1", "RM_meeting", livekit.EgressStatus_EGRESS_ACTIVE, at("10s"), at("10s")), at("10s")))
		batch = append(batch, roomWith(&ev, "RM_call", call, callSenders, "1s")...)
		batch = append(batch,
			ev.roomEnded("RM_meeting", "room-RM_meeting", at("0s"), at("40m"), livekit.RoomEndReason_ROOM_END_API_DELETE),
			ev.roomEnded("RM_call", "room-RM_call", at("0s"), at("50m"), livekit.RoomEndReason_ROOM_END_API_DELETE))
		env.apply(batch)

		for _, scope := range []string{"global", "node:" + testMediaNode} {
			hourly := find[schema.HourlyRollupDocument](env, schema.CollectionAnalyticsHourly, scope+":"+hourID)
			daily := find[schema.DailyRollupDocument](env, schema.CollectionAnalyticsDaily, scope+":"+dayID)
			for _, rooms := range []*schema.RollupRooms{hourly.Rooms, daily.Rooms} {
				require.Equal(t, map[string]int32{"many_to_many": 1, "one_to_one": 1}, rooms.ByTopology, scope)
				require.EqualValues(t, 1, *rooms.Recorded, scope)
				require.Equal(t, map[string]map[string]int32{"many_to_many": {"11-25": 1}}, rooms.SizeByTopology, scope)
			}
			fiveMinute := find[schema.FiveMinuteRollupDocument](env, schema.CollectionAnalytics5Min, scope+":"+hourID)
			require.NotNil(t, fiveMinute.Rooms)
		}
		require.Zero(t, count(env, schema.CollectionAnalytics5Min,
			bson.D{{Key: "rooms.byTopology", Value: bson.D{{Key: "$exists", Value: true}}}}))
	})

	t.Run("events of the last batch", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		participants, senders := standard("RM_1", 3, 1)
		env.apply(roomWith(&ev, "RM_1", participants, senders, "1s"))
		require.Equal(t, "one_to_many", find[schema.RoomDocument](env, schema.CollectionRooms, "RM_1").Topology)
		env.apply([]*livekit.AnalyticsEvent{
			ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_1", participants[1],
				microphoneTrack("TR_late"), at("5m")),
			ev.roomEnded("RM_1", "room-RM_1", at("0s"), at("6m"), livekit.RoomEndReason_ROOM_END_API_DELETE),
		})

		hourly := find[schema.HourlyRollupDocument](env, schema.CollectionAnalyticsHourly, "global:"+hourID)
		require.Equal(t, map[string]int32{"many_to_many": 1}, hourly.Rooms.ByTopology)
		require.Nil(t, hourly.Rooms.Recorded)
	})

	t.Run("replayed batch", func(t *testing.T) {
		env := newEnv(t, client)
		var ev events
		participants, senders := standard("RM_1", 4, 1)
		batch := append(roomWith(&ev, "RM_1", participants, senders, "1s"),
			ev.roomEnded("RM_1", "room-RM_1", at("0s"), at("6m"), livekit.RoomEndReason_ROOM_END_API_DELETE))
		env.apply(batch)
		env.p.cache = newRefCache()
		env.apply(batch)

		hourly := find[schema.HourlyRollupDocument](env, schema.CollectionAnalyticsHourly, "global:"+hourID)
		require.Equal(t, map[string]int32{"one_to_many": 1}, hourly.Rooms.ByTopology)
		require.Equal(t, map[string]map[string]int32{"one_to_many": {"2-5": 1}}, hourly.Rooms.SizeByTopology)
		require.EqualValues(t, 4, *hourly.Rooms.ParticipantsSum)
	})
}
