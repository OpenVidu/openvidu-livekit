// Copyright 2024 OpenVidu
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
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v3"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/openvidu/openvidu-livekit/openvidu/openviduconfig"
	"github.com/openvidu/openvidu-livekit/openvidu/queue"
)

var ANALYTICS_CONFIGURATION *openviduconfig.AnalyticsConfig
var ANALYTICS_SENDERS []*AnalyticsSender

type AnalyticsSender struct {
	eventsQueue        queue.Queue[*livekit.AnalyticsEvent]
	statsQueue         queue.Queue[*livekit.AnalyticsStat]
	icecandidatesQueue queue.Queue[*webrtc.ICECandidate]
	databaseClient     DatabaseClient
}

type DatabaseClient interface {
	InitializeDatabase() error
	SendBatch()
}

func InitializeAnalytics(configuration *config.Config) error {

	mongoDatabaseClient, err := NewMongoDatabaseClient(&configuration.OpenVidu.Analytics)
	if err != nil {
		return err
	}
	err = mongoDatabaseClient.InitializeDatabase()
	if err != nil {
		return err
	}
	ANALYTICS_CONFIGURATION = &configuration.OpenVidu.Analytics
	ANALYTICS_SENDERS = []*AnalyticsSender{mongoDatabaseClient.owner}

	// // To also store events and stats in Redis (given that it has module RedisJSON):
	//
	// redisDatabaseClient, err := NewRedisDatabaseClient(&configuration.OpenVidu.Analytics, &configuration.Redis)
	// if err != nil {
	// 	return err
	// }
	// err = redisDatabaseClient.InitializeDatabase()
	// if err != nil {
	// 	return err
	// }
	// ANALYTICS_SENDERS = append(ANALYTICS_SENDERS, redisDatabaseClient.owner)

	return nil
}

// Blocking method. Launch in goroutine
func Start() {
	var wg sync.WaitGroup
	wg.Add(1)
	go startAnalyticsRoutine()
	wg.Wait()
}

func startAnalyticsRoutine() {
	for {
		time.Sleep(ANALYTICS_CONFIGURATION.Interval)
		sendBatch()
	}
}

func sendBatch() {
	for _, sender := range ANALYTICS_SENDERS {
		sender.databaseClient.SendBatch()
	}
}

type OpenViduEventsIngestClient struct {
	// Must have this empty property to implement interface livekit.AnalyticsRecorderService_IngestEventsClient
	grpc.ClientStream
}

type OpenViduStatsIngestClient struct {
	// Must have this empty property to implement interface livekit.AnalyticsRecorderService_IngestStatsClient
	grpc.ClientStream
}

type OpenViduIcecandidatesIngestClient struct {
}

func NewOpenViduEventsIngestClient() OpenViduEventsIngestClient {
	return OpenViduEventsIngestClient{}
}

func NewOpenViduStatsIngestClient() OpenViduStatsIngestClient {
	return OpenViduStatsIngestClient{}
}

func NewOpenViduIcecandidatesIngestClient() OpenViduIcecandidatesIngestClient {
	return OpenViduIcecandidatesIngestClient{}
}

func (client OpenViduEventsIngestClient) Send(events *livekit.AnalyticsEvents) error {
	logger.Debugw("adding " + strconv.Itoa(len(events.Events)) + " new events to next batch")
	logger.Debugw(events.String())
	for _, sender := range ANALYTICS_SENDERS {
		for _, event := range events.Events {
			sender.eventsQueue.Enqueue(event)
		}
	}
	return nil
}

func (client OpenViduStatsIngestClient) Send(stats *livekit.AnalyticsStats) error {
	logger.Debugw("adding " + strconv.Itoa(len(stats.Stats)) + " new stats to next batch")
	logger.Debugw(stats.String())
	for _, sender := range ANALYTICS_SENDERS {
		for _, stat := range stats.Stats {
			sender.statsQueue.Enqueue(stat)
		}
	}
	return nil
}

func (client OpenViduIcecandidatesIngestClient) Send(icecandidate *webrtc.ICECandidate) error {
	logger.Debugw("adding new ICE candidate to next batch")
	logger.Debugw(icecandidate.String())
	for _, sender := range ANALYTICS_SENDERS {
		sender.icecandidatesQueue.Enqueue(icecandidate)
	}
	return nil
}

// We don't implement grpc, so this is an empty method
func (client OpenViduEventsIngestClient) CloseAndRecv() (*emptypb.Empty, error) {
	return nil, nil
}

// We don't implement grpc, so this is an empty method
func (client OpenViduStatsIngestClient) CloseAndRecv() (*emptypb.Empty, error) {
	return nil, nil
}

func dequeEvents(eventsQueue queue.Queue[*livekit.AnalyticsEvent]) []*livekit.AnalyticsEvent {
	var result []*livekit.AnalyticsEvent
	for eventsQueue.Len() > 0 {
		event, _ := eventsQueue.Dequeue()
		result = append(result, event)
	}
	return result
}

func dequeStats(statsQueue queue.Queue[*livekit.AnalyticsStat]) []*livekit.AnalyticsStat {
	var result []*livekit.AnalyticsStat
	for statsQueue.Len() > 0 {
		stat, _ := statsQueue.Dequeue()
		result = append(result, stat)
	}
	return result
}

func dequeIcecandidates(icecandidatesQueue queue.Queue[*webrtc.ICECandidate]) []*webrtc.ICECandidate {
	var result []*webrtc.ICECandidate
	for icecandidatesQueue.Len() > 0 {
		icecandidate, _ := icecandidatesQueue.Dequeue()
		result = append(result, icecandidate)
	}
	return result
}

func obtainMapInterfaceFromEvent(event *livekit.AnalyticsEvent) map[string]interface{} {
	var eventMap map[string]interface{}
	var eventBytes []byte
	eventBytes, _ = json.Marshal(event)
	json.Unmarshal(eventBytes, &eventMap)
	return eventMap
}

func obtainMapInterfaceFromStat(stat *livekit.AnalyticsStat) map[string]interface{} {
	var statMap map[string]interface{}
	var statBytes []byte
	statBytes, _ = json.Marshal(stat)
	json.Unmarshal(statBytes, &statMap)
	return statMap
}

func obtainMapInterfaceFromIcecandidate(icecandidate *webrtc.ICECandidate) map[string]interface{} {
	var icecandidateMap map[string]interface{}
	var icecandidateBytes []byte
	icecandidateBytes, _ = json.Marshal(icecandidate)
	json.Unmarshal(icecandidateBytes, &icecandidateMap)
	return icecandidateMap
}

func getTimestampFromStruct(timestamp *timestamppb.Timestamp) string {
	var timestampKey string = strconv.FormatInt(timestamp.Seconds, 10)
	if timestamp.Nanos > 0 {
		timestampKey += strconv.FormatInt(int64(timestamp.Nanos), 10)
	}
	return timestampKey
}

func parseEvent(eventMap map[string]interface{}, event *livekit.AnalyticsEvent) {
	eventMap["type"] = event.Type.String()
	if eventMap["participant"] != nil {
		eventMap["participant"].(map[string]interface{})["state"] = event.Participant.State.String()
	}
	if eventMap["client_info"] != nil {
		eventMap["client_info"].(map[string]interface{})["sdk"] = event.ClientInfo.Sdk.String()
	}
	if eventMap["egress"] != nil {
		eventMap["egress"].(map[string]interface{})["status"] = event.Egress.Status.String()

		if eventMap["egress"].(map[string]interface{})["Request"] != nil {
			parseEgressRequest(
				eventMap["egress"].(map[string]interface{})["Request"].(map[string]interface{}),
				event.Egress.Request,
			)
		}
	}
	if eventMap["ingress"] != nil && eventMap["ingress"].(map[string]interface{})["state"] != nil {
		eventMap["ingress"].(map[string]interface{})["state"].(map[string]interface{})["status"] =
			event.Ingress.State.Status.String()
	}
	if eventMap["track"] != nil {
		eventMap["track"].(map[string]interface{})["source"] = event.Track.Source.String()
	}
}

func parseEgressRequest(egressRequestMap map[string]interface{}, egressRequest interface{}) {
	switch egressRequest.(type) {
	case *livekit.EgressInfo_RoomComposite:
		parseRoomCompositeEgressRequest(
			egressRequestMap["RoomComposite"].(map[string]interface{}),
			egressRequest.(*livekit.EgressInfo_RoomComposite).RoomComposite,
		)
	case *livekit.EgressInfo_Web:
		parseWebEgressRequest(
			egressRequestMap["Web"].(map[string]interface{}),
			egressRequest.(*livekit.EgressInfo_Web).Web,
		)
	case *livekit.EgressInfo_Participant:
		parseParticipantEgressRequest(
			egressRequestMap["Participant"].(map[string]interface{}),
			egressRequest.(*livekit.EgressInfo_Participant).Participant,
		)
	case *livekit.EgressInfo_TrackComposite:
		parseTrackCompositeEgressRequest(
			egressRequestMap["TrackComposite"].(map[string]interface{}),
			egressRequest.(*livekit.EgressInfo_TrackComposite).TrackComposite,
		)
	}
}

func parseRoomCompositeEgressRequest(roomCompositeMap map[string]interface{}, roomComposite *livekit.RoomCompositeEgressRequest) {
	var options = roomComposite.Options
	if options != nil {
		switch options.(type) {
		case *livekit.RoomCompositeEgressRequest_Preset:
			roomCompositeMap["Options"].(map[string]interface{})["Preset"] =
				options.(*livekit.RoomCompositeEgressRequest_Preset).Preset.String()
		case *livekit.RoomCompositeEgressRequest_Advanced:
			var advancedOptions = options.(*livekit.RoomCompositeEgressRequest_Advanced).Advanced
			if advancedOptions != nil {
				parseEncodingOptions(roomCompositeMap, advancedOptions)
			}
		}
	}

	parseEgressRequestOutputs(
		roomCompositeMap,
		roomComposite.FileOutputs,
		roomComposite.StreamOutputs,
		roomComposite.SegmentOutputs,
		roomComposite.ImageOutputs,
	)
}

func parseWebEgressRequest(webRequestMap map[string]interface{}, webRequest *livekit.WebEgressRequest) {
	var options = webRequest.Options
	if options != nil {
		switch options.(type) {
		case *livekit.WebEgressRequest_Preset:
			webRequestMap["Options"].(map[string]interface{})["Preset"] =
				options.(*livekit.WebEgressRequest_Preset).Preset.String()
		case *livekit.WebEgressRequest_Advanced:
			var advancedOptions = options.(*livekit.WebEgressRequest_Advanced).Advanced
			if advancedOptions != nil {
				parseEncodingOptions(webRequestMap, advancedOptions)
			}
		}
	}

	parseEgressRequestOutputs(
		webRequestMap,
		webRequest.FileOutputs,
		webRequest.StreamOutputs,
		webRequest.SegmentOutputs,
		webRequest.ImageOutputs,
	)
}

func parseParticipantEgressRequest(participantRequestMap map[string]interface{}, participantRequest *livekit.ParticipantEgressRequest) {
	var options = participantRequest.Options
	if options != nil {
		switch options.(type) {
		case *livekit.ParticipantEgressRequest_Preset:
			participantRequestMap["Options"].(map[string]interface{})["Preset"] =
				options.(*livekit.ParticipantEgressRequest_Preset).Preset.String()
		case *livekit.ParticipantEgressRequest_Advanced:
			var advancedOptions = options.(*livekit.ParticipantEgressRequest_Advanced).Advanced
			if advancedOptions != nil {
				parseEncodingOptions(participantRequestMap, advancedOptions)
			}
		}
	}

	parseEgressRequestOutputs(
		participantRequestMap,
		participantRequest.FileOutputs,
		participantRequest.StreamOutputs,
		participantRequest.SegmentOutputs,
		participantRequest.ImageOutputs,
	)
}

func parseTrackCompositeEgressRequest(trackCompositeMap map[string]interface{}, trackComposite *livekit.TrackCompositeEgressRequest) {
	var options = trackComposite.Options
	if options != nil {
		switch options.(type) {
		case *livekit.TrackCompositeEgressRequest_Preset:
			trackCompositeMap["Options"].(map[string]interface{})["Preset"] =
				options.(*livekit.TrackCompositeEgressRequest_Preset).Preset.String()
		case *livekit.TrackCompositeEgressRequest_Advanced:
			var advancedOptions = options.(*livekit.TrackCompositeEgressRequest_Advanced).Advanced
			if advancedOptions != nil {
				parseEncodingOptions(trackCompositeMap, advancedOptions)
			}
		}
	}

	parseEgressRequestOutputs(
		trackCompositeMap,
		trackComposite.FileOutputs,
		trackComposite.StreamOutputs,
		trackComposite.SegmentOutputs,
		trackComposite.ImageOutputs,
	)
}

func parseEncodingOptions(egressRequestMap map[string]interface{}, advancedOptions *livekit.EncodingOptions) {
	egressRequestMap["Options"].(map[string]interface{})["Advanced"].(map[string]interface{})["audio_codec"] =
		advancedOptions.AudioCodec.String()
	egressRequestMap["Options"].(map[string]interface{})["Advanced"].(map[string]interface{})["video_codec"] =
		advancedOptions.VideoCodec.String()
}

func parseEgressRequestOutputs(
	egressRequestMap map[string]interface{},
	fileOutputs []*livekit.EncodedFileOutput,
	streamOutputs []*livekit.StreamOutput,
	segmentOutputs []*livekit.SegmentedFileOutput,
	imageOutputs []*livekit.ImageOutput,
) {
	for i, fileOutput := range fileOutputs {
		egressRequestMap["file_outputs"].([]interface{})[i].(map[string]interface{})["file_type"] =
			fileOutput.FileType.String()
	}

	for i, streamOutput := range streamOutputs {
		egressRequestMap["stream_outputs"].([]interface{})[i].(map[string]interface{})["protocol"] =
			streamOutput.Protocol.String()
	}

	for i, segmentOutput := range segmentOutputs {
		egressRequestMap["segment_outputs"].([]interface{})[i].(map[string]interface{})["protocol"] =
			segmentOutput.Protocol.String()
		egressRequestMap["segment_outputs"].([]interface{})[i].(map[string]interface{})["filename_suffix"] =
			segmentOutput.FilenameSuffix.String()
	}

	for i, imageOutput := range imageOutputs {
		egressRequestMap["image_outputs"].([]interface{})[i].(map[string]interface{})["filename_suffix"] =
			imageOutput.FilenameSuffix.String()
		egressRequestMap["image_outputs"].([]interface{})[i].(map[string]interface{})["image_codec"] =
			imageOutput.ImageCodec.String()
	}
}

func parseStat(statMap map[string]interface{}, stat *livekit.AnalyticsStat) {
	statMap["kind"] = stat.Kind.String()
}
