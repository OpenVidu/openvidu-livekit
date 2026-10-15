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
	"fmt"
	"time"

	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Builders of protocol events shaped as the server emits them.

const testNode = "ND_test"

var baseTime = time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

// at returns baseTime plus an offset such as "1m30s".
func at(offset string) time.Time {
	d, err := time.ParseDuration(offset)
	if err != nil {
		panic(err)
	}
	return baseTime.Add(d)
}

type events struct {
	sequence int
}

func (e *events) next(eventType livekit.AnalyticsEventType, when time.Time) *livekit.AnalyticsEvent {
	e.sequence++
	return &livekit.AnalyticsEvent{
		Id:        fmt.Sprintf("AE_%s_%04d", eventType.String(), e.sequence),
		Type:      eventType,
		Timestamp: timestamppb.New(when),
		NodeId:    testNode,
	}
}

func roomInfo(sid string, name string, created time.Time) *livekit.Room {
	return &livekit.Room{
		Sid: sid, Name: name, EmptyTimeout: 300, DepartureTimeout: 20, MaxParticipants: 10,
		CreationTime: created.Unix(), CreationTimeMs: created.UnixMilli(), TurnPassword: "SECRET-turn",
		Metadata: "room metadata",
	}
}

func (e *events) roomCreated(sid string, name string, when time.Time) *livekit.AnalyticsEvent {
	event := e.next(livekit.AnalyticsEventType_ROOM_CREATED, when)
	event.Room = roomInfo(sid, name, when)
	return event
}

func (e *events) roomEnded(sid string, name string, created time.Time, when time.Time, reason livekit.RoomEndReason) *livekit.AnalyticsEvent {
	event := e.next(livekit.AnalyticsEventType_ROOM_ENDED, when)
	event.RoomId = sid
	event.Room = roomInfo(sid, name, created)
	event.RoomEndReason = reason
	return event
}

func participantInfo(sid string, identity string, kind livekit.ParticipantInfo_Kind, joined time.Time) *livekit.ParticipantInfo {
	return &livekit.ParticipantInfo{
		Sid: sid, Identity: identity, Name: identity, Kind: kind,
		JoinedAt: joined.Unix(), JoinedAtMs: joined.UnixMilli(),
		Permission: &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: true, CanPublishData: true},
		Region:     "eu-west", Metadata: "participant metadata", Attributes: map[string]string{"role": "host"},
	}
}

func (e *events) participant(eventType livekit.AnalyticsEventType, room string, info *livekit.ParticipantInfo, when time.Time) *livekit.AnalyticsEvent {
	event := e.next(eventType, when)
	event.RoomId = room
	event.Room = &livekit.Room{Sid: room, Name: "room-" + room}
	event.ParticipantId = info.GetSid()
	event.Participant = info
	switch eventType {
	case livekit.AnalyticsEventType_PARTICIPANT_JOINED:
		event.ClientInfo = &livekit.ClientInfo{
			Sdk: livekit.ClientInfo_JS, Version: "2.15.7", Os: "Windows", OsVersion: "11", Browser: "Chrome",
			BrowserVersion: "130.0", Address: "203.0.113.10", Network: "wifi",
		}
		event.ClientMeta = &livekit.AnalyticsClientMeta{Region: "eu-west", Node: testNode, ClientAddr: "203.0.113.10"}
	case livekit.AnalyticsEventType_PARTICIPANT_ACTIVE:
		event.ClientMeta = &livekit.AnalyticsClientMeta{ClientConnectTime: 1200, ConnectionType: "udp"}
	}
	return event
}

func (e *events) joined(room string, info *livekit.ParticipantInfo, when time.Time) *livekit.AnalyticsEvent {
	return e.participant(livekit.AnalyticsEventType_PARTICIPANT_JOINED, room, info, when)
}

func (e *events) active(room string, info *livekit.ParticipantInfo, when time.Time) *livekit.AnalyticsEvent {
	return e.participant(livekit.AnalyticsEventType_PARTICIPANT_ACTIVE, room, info, when)
}

func (e *events) left(room string, info *livekit.ParticipantInfo, when time.Time, reason livekit.DisconnectReason) *livekit.AnalyticsEvent {
	copied := cloneParticipant(info)
	copied.State = livekit.ParticipantInfo_DISCONNECTED
	copied.DisconnectReason = reason
	return e.participant(livekit.AnalyticsEventType_PARTICIPANT_LEFT, room, copied, when)
}

func (e *events) aborted(room string, info *livekit.ParticipantInfo, when time.Time, reason livekit.DisconnectReason) *livekit.AnalyticsEvent {
	copied := cloneParticipant(info)
	copied.State = livekit.ParticipantInfo_DISCONNECTED
	copied.DisconnectReason = reason
	return e.participant(livekit.AnalyticsEventType_PARTICIPANT_CONNECTION_ABORTED, room, copied, when)
}

func cloneParticipant(info *livekit.ParticipantInfo) *livekit.ParticipantInfo {
	return proto.Clone(info).(*livekit.ParticipantInfo)
}

func cameraTrack(sid string) *livekit.TrackInfo {
	return &livekit.TrackInfo{
		Sid: sid, Type: livekit.TrackType_VIDEO, Source: livekit.TrackSource_CAMERA, Name: "camera",
		MimeType: "video/VP8", Width: 1280, Height: 720, Simulcast: true,
		Codecs: []*livekit.SimulcastCodecInfo{{MimeType: "video/VP8", Layers: []*livekit.VideoLayer{
			{Quality: livekit.VideoQuality_LOW, Width: 320, Height: 180, Bitrate: 150000},
			{Quality: livekit.VideoQuality_HIGH, Width: 1280, Height: 720, Bitrate: 1700000},
		}}},
	}
}

func microphoneTrack(sid string) *livekit.TrackInfo {
	return &livekit.TrackInfo{Sid: sid, Type: livekit.TrackType_AUDIO, Source: livekit.TrackSource_MICROPHONE, Name: "microphone", MimeType: "audio/opus"}
}

func (e *events) track(eventType livekit.AnalyticsEventType, room string, participant *livekit.ParticipantInfo, track *livekit.TrackInfo, when time.Time) *livekit.AnalyticsEvent {
	event := e.next(eventType, when)
	event.RoomId = room
	event.Room = &livekit.Room{Sid: room, Name: "room-" + room}
	event.ParticipantId = participant.GetSid()
	event.Participant = &livekit.ParticipantInfo{Sid: participant.GetSid()}
	if eventType == livekit.AnalyticsEventType_TRACK_PUBLISHED {
		event.Participant.Identity = participant.GetIdentity()
	}
	event.TrackId = track.GetSid()
	event.Track = track
	return event
}

func (e *events) subscription(eventType livekit.AnalyticsEventType, room string, subscriber string, track *livekit.TrackInfo, publisher *livekit.ParticipantInfo, when time.Time) *livekit.AnalyticsEvent {
	event := e.next(eventType, when)
	event.RoomId = room
	event.Room = &livekit.Room{Sid: room, Name: "room-" + room}
	event.ParticipantId = subscriber
	event.Participant = &livekit.ParticipantInfo{Sid: subscriber}
	event.TrackId = track.GetSid()
	switch eventType {
	case livekit.AnalyticsEventType_TRACK_SUBSCRIBED:
		event.Track = track
		event.Publisher = &livekit.ParticipantInfo{Sid: publisher.GetSid(), Identity: publisher.GetIdentity()}
	case livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED:
		event.Track = &livekit.TrackInfo{Sid: track.GetSid(), Type: track.GetType()}
	case livekit.AnalyticsEventType_TRACK_SUBSCRIBE_FAILED:
		event.Track = &livekit.TrackInfo{Sid: track.GetSid()}
		event.Error = "could not find published track"
	default:
		event.Track = &livekit.TrackInfo{Sid: track.GetSid()}
	}
	return event
}

func (e *events) egress(eventType livekit.AnalyticsEventType, info *livekit.EgressInfo, when time.Time) *livekit.AnalyticsEvent {
	event := e.next(eventType, when)
	event.EgressId = info.GetEgressId()
	event.RoomId = info.GetRoomId()
	event.Egress = info
	return event
}

func roomCompositeEgress(id string, room string, status livekit.EgressStatus, started time.Time, updated time.Time) *livekit.EgressInfo {
	return &livekit.EgressInfo{
		EgressId: id, RoomId: room, RoomName: "room-" + room, Status: status,
		StartedAt: started.UnixNano(), UpdatedAt: updated.UnixNano(),
		Request: &livekit.EgressInfo_RoomComposite{RoomComposite: &livekit.RoomCompositeEgressRequest{
			RoomName: "room-" + room, Layout: "speaker",
			Options: &livekit.RoomCompositeEgressRequest_Preset{Preset: livekit.EncodingOptionsPreset_H264_720P_30},
			FileOutputs: []*livekit.EncodedFileOutput{{
				Filepath: "recordings/room.mp4",
				Output: &livekit.EncodedFileOutput_S3{S3: &livekit.S3Upload{
					AccessKey: "SECRET-access", Secret: "SECRET-secret", SessionToken: "SECRET-token", Bucket: "bucket",
				}},
			}},
			StreamOutputs: []*livekit.StreamOutput{{Urls: []string{"rtmps://user:pw@live.example.com/app/live_SECRET?token=abc"}}},
		}},
	}
}

func (e *events) ingress(eventType livekit.AnalyticsEventType, info *livekit.IngressInfo, when time.Time) *livekit.AnalyticsEvent {
	event := e.next(eventType, when)
	event.IngressId = info.GetIngressId()
	event.Ingress = info
	return event
}

func rtmpIngress(id string, room string, state *livekit.IngressState) *livekit.IngressInfo {
	return &livekit.IngressInfo{
		IngressId: id, Name: "OBS", StreamKey: "SECRET-streamkey", Url: "rtmp://ingress.example.com/x",
		InputType: livekit.IngressInput_RTMP_INPUT, RoomName: room, ParticipantIdentity: "obs", Reusable: true,
		State: state,
	}
}

func ingressState(status livekit.IngressState_Status, room string, resource string, started time.Time, ended time.Time) *livekit.IngressState {
	state := &livekit.IngressState{Status: status, RoomId: room, ResourceId: resource, StartedAt: started.UnixNano(),
		UpdatedAt: started.UnixNano()}
	if !ended.IsZero() {
		state.EndedAt = ended.UnixNano()
		state.UpdatedAt = ended.UnixNano()
	}
	return state
}

func stat(room string, participant string, track string, kind livekit.StreamType, when time.Time, score float32, bytes uint64) *livekit.AnalyticsStat {
	return &livekit.AnalyticsStat{
		Id: fmt.Sprintf("AS_%s_%s_%d", participant, track, when.Unix()), Kind: kind, TimeStamp: timestamppb.New(when),
		Node: testNode, RoomId: room, ParticipantId: participant, TrackId: track, Score: score, MinScore: score,
		MedianScore: score, Mime: "video/VP8",
		Streams: []*livekit.AnalyticsStream{{PrimaryBytes: bytes, PrimaryPackets: 100, PacketsLost: 1, Rtt: 40, Jitter: 2500,
			Frames: 30, VideoLayers: []*livekit.AnalyticsVideoLayer{{Layer: 0, Bytes: bytes, Packets: 100, Frames: 30}}}},
	}
}
