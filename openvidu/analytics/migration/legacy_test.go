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
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/proto"
)

// The legacy writer, as it encoded documents before the v2 writer replaced it (openvidu/analytics/analytics.go and
// mongo.go before the v2 writer): encoding/json round-trip of the protocol struct into a map, some enums rewritten to
// their names, an md5 _id and openvidu_expire_at. Tests use it to produce legacy documents of every event type the
// recorded fixtures cover.

var legacyExpireAt = time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)

func legacyEventDocument(t *testing.T, event *livekit.AnalyticsEvent) bson.Raw {
	t.Helper()
	document := legacyMap(t, event)
	legacyParseEvent(document, event)
	key := ""
	if event.Room != nil {
		key += event.Room.Sid + ":"
	}
	if event.ParticipantId != "" {
		key += event.ParticipantId + ":"
	}
	if event.TrackId != "" {
		key += event.TrackId + ":"
	}
	key += event.Type.String() + ":" + legacyTimestampKey(event.GetTimestamp().GetSeconds(), event.GetTimestamp().GetNanos())
	document["_id"] = legacyHash(key)
	document["openvidu_expire_at"] = legacyExpireAt
	raw, err := bson.Marshal(document)
	require.NoError(t, err)
	return raw
}

func legacyStatDocument(t *testing.T, stat *livekit.AnalyticsStat) bson.Raw {
	t.Helper()
	document := legacyMap(t, stat)
	document["kind"] = stat.Kind.String()
	key := stat.RoomId + ":" + stat.ParticipantId + ":" + stat.TrackId + ":" + stat.Kind.String() + ":" + stat.Node + ":" +
		legacyTimestampKey(stat.GetTimeStamp().GetSeconds(), stat.GetTimeStamp().GetNanos())
	document["_id"] = legacyHash(key)
	document["openvidu_expire_at"] = legacyExpireAt
	raw, err := bson.Marshal(document)
	require.NoError(t, err)
	return raw
}

func legacyMap(t *testing.T, message any) map[string]any {
	data, err := json.Marshal(message)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(data, &document))
	return document
}

func legacyTimestampKey(seconds int64, nanos int32) string {
	key := strconv.FormatInt(seconds, 10)
	if nanos > 0 {
		key += strconv.FormatInt(int64(nanos), 10)
	}
	return key
}

func legacyHash(key string) string {
	hash := md5.Sum([]byte(key))
	return hex.EncodeToString(hash[:])
}

func legacyParseEvent(document map[string]any, event *livekit.AnalyticsEvent) {
	document["type"] = event.Type.String()
	if participant, ok := document["participant"].(map[string]any); ok {
		participant["state"] = event.Participant.State.String()
	}
	if client, ok := document["client_info"].(map[string]any); ok {
		client["sdk"] = event.ClientInfo.Sdk.String()
	}
	if egress, ok := document["egress"].(map[string]any); ok {
		egress["status"] = event.Egress.Status.String()
		if request, ok := egress["Request"].(map[string]any); ok {
			legacyParseEgressRequest(request, event.Egress.Request)
		}
	}
	if ingress, ok := document["ingress"].(map[string]any); ok {
		if state, ok := ingress["state"].(map[string]any); ok {
			state["status"] = event.Ingress.State.Status.String()
		}
	}
	if track, ok := document["track"].(map[string]any); ok {
		track["source"] = event.Track.Source.String()
	}
}

type legacyRequest struct {
	key      string
	preset   *livekit.EncodingOptionsPreset
	advanced *livekit.EncodingOptions
	files    []*livekit.EncodedFileOutput
	streams  []*livekit.StreamOutput
	segments []*livekit.SegmentedFileOutput
	images   []*livekit.ImageOutput
}

func legacyParseEgressRequest(document map[string]any, request any) {
	var r legacyRequest
	switch v := request.(type) {
	case *livekit.EgressInfo_RoomComposite:
		r = legacyRequest{key: "RoomComposite", files: v.RoomComposite.FileOutputs, streams: v.RoomComposite.StreamOutputs,
			segments: v.RoomComposite.SegmentOutputs, images: v.RoomComposite.ImageOutputs}
		if preset, ok := v.RoomComposite.Options.(*livekit.RoomCompositeEgressRequest_Preset); ok {
			r.preset = &preset.Preset
		}
		r.advanced = v.RoomComposite.GetAdvanced()
	case *livekit.EgressInfo_Web:
		r = legacyRequest{key: "Web", files: v.Web.FileOutputs, streams: v.Web.StreamOutputs,
			segments: v.Web.SegmentOutputs, images: v.Web.ImageOutputs}
		if preset, ok := v.Web.Options.(*livekit.WebEgressRequest_Preset); ok {
			r.preset = &preset.Preset
		}
		r.advanced = v.Web.GetAdvanced()
	case *livekit.EgressInfo_Participant:
		r = legacyRequest{key: "Participant", files: v.Participant.FileOutputs, streams: v.Participant.StreamOutputs,
			segments: v.Participant.SegmentOutputs, images: v.Participant.ImageOutputs}
		if preset, ok := v.Participant.Options.(*livekit.ParticipantEgressRequest_Preset); ok {
			r.preset = &preset.Preset
		}
		r.advanced = v.Participant.GetAdvanced()
	case *livekit.EgressInfo_TrackComposite:
		r = legacyRequest{key: "TrackComposite", files: v.TrackComposite.FileOutputs,
			streams: v.TrackComposite.StreamOutputs, segments: v.TrackComposite.SegmentOutputs,
			images: v.TrackComposite.ImageOutputs}
		if preset, ok := v.TrackComposite.Options.(*livekit.TrackCompositeEgressRequest_Preset); ok {
			r.preset = &preset.Preset
		}
		r.advanced = v.TrackComposite.GetAdvanced()
	default:
		return
	}
	inner, ok := document[r.key].(map[string]any)
	if !ok {
		return
	}
	if options, ok := inner["Options"].(map[string]any); ok {
		if r.preset != nil {
			options["Preset"] = r.preset.String()
		}
		if advanced, ok := options["Advanced"].(map[string]any); ok && r.advanced != nil {
			advanced["audio_codec"] = r.advanced.AudioCodec.String()
			advanced["video_codec"] = r.advanced.VideoCodec.String()
		}
	}
	for i, output := range r.files {
		inner["file_outputs"].([]any)[i].(map[string]any)["file_type"] = output.FileType.String()
	}
	for i, output := range r.streams {
		inner["stream_outputs"].([]any)[i].(map[string]any)["protocol"] = output.Protocol.String()
	}
	for i, output := range r.segments {
		segment := inner["segment_outputs"].([]any)[i].(map[string]any)
		segment["protocol"] = output.Protocol.String()
		segment["filename_suffix"] = output.FilenameSuffix.String()
	}
	for i, output := range r.images {
		image := inner["image_outputs"].([]any)[i].(map[string]any)
		image["filename_suffix"] = output.FilenameSuffix.String()
		image["image_codec"] = output.ImageCodec.String()
	}
}

// recordedLegacy reads a collection of testdata/legacy, one canonical Extended JSON document per line.
func recordedLegacy(t *testing.T, collection string) []bson.Raw {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "legacy", collection+".json"))
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

func cloneParticipant(info *livekit.ParticipantInfo) *livekit.ParticipantInfo {
	return proto.Clone(info).(*livekit.ParticipantInfo)
}

func cloneEgress(info *livekit.EgressInfo) *livekit.EgressInfo {
	return proto.Clone(info).(*livekit.EgressInfo)
}
