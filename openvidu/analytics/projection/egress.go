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

package projection

import (
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

const (
	egressRoomComposite  = "room_composite"
	egressWeb            = "web"
	egressParticipant    = "participant"
	egressTrackComposite = "track_composite"
	egressTrack          = "track"

	statusHistoryLimit = 50
)

var egressRanks = []rank{
	{"starting", 0}, {"active", 1}, {"ending", 2},
	{"complete", 3}, {"failed", 3}, {"aborted", 3}, {"limit_reached", 3},
}

var egressTerminal = bson.A{"complete", "failed", "aborted", "limit_reached"}

func (p *projector) egress() {
	info := p.event.GetEgress()
	id := firstNonEmpty(p.event.GetEgressId(), info.GetEgressId())
	if id == "" {
		return
	}
	status := strings.ToLower(enumName(info.GetStatus().String(), "EGRESS_"))
	kind := egressType(info)
	updatedAt := orTime(schema.UnixNanos(info.GetUpdatedAt()), p.ts)
	roomSid := info.GetRoomId()

	f := newFields()
	if roomSid != "" {
		f.set("roomSid", keep("roomSid", roomSid))
	}
	if info.GetRoomName() != "" {
		f.set("roomName", keep("roomName", info.GetRoomName()))
		f.set("roomNameLower", keep("roomNameLower", strings.ToLower(info.GetRoomName())))
	}
	f.set("type", keep("type", kind))
	f.set("sourceType", keep("sourceType", strings.ToLower(enumName(info.GetSourceType().String(), "EGRESS_SOURCE_TYPE_"))))
	f.set("status", ranked("status", status, egressRanks))
	f.set("startedAt", minDate("startedAt", orTime(schema.UnixNanos(info.GetStartedAt()), p.ts)))
	f.set("updatedAt", maxDate("updatedAt", updatedAt))
	if info.GetEndedAt() != 0 {
		f.set("endedAt", keep("endedAt", schema.UnixNanos(info.GetEndedAt())))
	}
	if info.GetError() != "" {
		f.set("error", info.GetError())
	}
	if info.GetErrorCode() != 0 {
		f.set("errorCode", info.GetErrorCode())
	}
	f.set("request", egressRequest(info, p.opts.Encode))
	if raw := egressRawRequest(info, p.opts.Encode); raw != nil {
		f.set("rawRequest", raw)
	}
	if outputs := egressOutputs(info); len(outputs) > 0 {
		f.set("outputs", latest("outputs", "updatedAt", updatedAt, outputs))
	}
	f.set("statusHistory", withStatus(status, updatedAt))
	f.set("retryCount", maxOf("retryCount", info.GetRetryCount()))
	f.set("backupStorageUsed", or(keep("backupStorageUsed", false), info.GetBackupStorageUsed()))
	if isTerminal(status) {
		f.set("closedBy", byEvent)
	}
	f.set("schemaVersion", int32(schema.SchemaVersion))
	p.upsert(schema.CollectionEgresses, id, f)

	if roomSid != "" {
		room := p.roomTouch(info.GetRoomName())
		room.set("internal.egressIds", union("internal.egressIds", id))
		room.set("internal.egressByType."+kind, union("internal.egressByType."+kind, id))
		if p.event.GetType() == livekit.AnalyticsEventType_EGRESS_ENDED || isTerminal(status) {
			room.set("internal.endedEgressIds", union("internal.endedEgressIds", id))
		}
		p.upsert(schema.CollectionRooms, roomSid, room)
	}
}

// withStatus adds a status change to the egress status history, kept sorted by time and bounded.
func withStatus(status string, at time.Time) bson.D {
	entry := bson.D{{Key: "status", Value: status}, {Key: "at", Value: at}}
	return bson.D{{Key: "$slice", Value: bson.A{
		bson.D{{Key: "$sortArray", Value: bson.D{
			{Key: "input", Value: union("statusHistory", entry)},
			{Key: "sortBy", Value: bson.D{{Key: "at", Value: 1}}},
		}}},
		-statusHistoryLimit,
	}}}
}

func isTerminal(status string) bool {
	for _, terminal := range egressTerminal {
		if terminal == status {
			return true
		}
	}
	return false
}

func orTime(t time.Time, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

// egressType maps the request of an egress to its type; requests of the unified StartEgress API map by their source.
func egressType(info *livekit.EgressInfo) string {
	switch request := info.GetRequest().(type) {
	case *livekit.EgressInfo_RoomComposite:
		return egressRoomComposite
	case *livekit.EgressInfo_Web:
		return egressWeb
	case *livekit.EgressInfo_Participant:
		return egressParticipant
	case *livekit.EgressInfo_TrackComposite:
		return egressTrackComposite
	case *livekit.EgressInfo_Track:
		return egressTrack
	case *livekit.EgressInfo_Egress:
		return sourceType(request.Egress.GetTemplate() != nil, request.Egress.GetWeb() != nil, request.Egress.GetMedia())
	case *livekit.EgressInfo_Replay:
		return sourceType(request.Replay.GetTemplate() != nil, request.Replay.GetWeb() != nil, request.Replay.GetMedia())
	}
	if info.GetRoomId() == "" {
		return egressWeb
	}
	return egressRoomComposite
}

func sourceType(template bool, web bool, media *livekit.MediaSource) string {
	switch {
	case template:
		return egressRoomComposite
	case web:
		return egressWeb
	case media.GetParticipantVideo() != nil:
		return egressParticipant
	default:
		return egressTrackComposite
	}
}

// egressRequest summarizes the request without credentials: layout, sources, encoding and the kinds of output.
func egressRequest(info *livekit.EgressInfo, encode schema.EncodeOptions) bson.D {
	summary := newFields()
	flag := func(key string, value bool) {
		if value {
			summary.set(key, true)
		}
	}
	text := func(key string, value string) {
		if value != "" {
			summary.set(key, value)
		}
	}
	encoding := func(preset livekit.EncodingOptionsPreset, advanced *livekit.EncodingOptions) {
		if advanced != nil {
			summary.set("advanced", schema.Encode(advanced, encode))
		} else {
			summary.set("preset", preset.String())
		}
	}
	var outputs []string
	addOutputs := func(file bool, stream bool, segments bool, images bool) {
		for _, output := range []struct {
			kind    string
			present bool
		}{{"file", file}, {"stream", stream}, {"segments", segments}, {"images", images}} {
			if output.present && !containsString(outputs, output.kind) {
				outputs = append(outputs, output.kind)
			}
		}
	}

	switch request := info.GetRequest().(type) {
	case *livekit.EgressInfo_RoomComposite:
		r := request.RoomComposite
		text("layout", r.GetLayout())
		text("customBaseUrl", redactURL(r.GetCustomBaseUrl()))
		flag("audioOnly", r.GetAudioOnly())
		flag("videoOnly", r.GetVideoOnly())
		encoding(r.GetPreset(), r.GetAdvanced())
		addOutputs(len(r.GetFileOutputs()) > 0 || r.GetFile() != nil, len(r.GetStreamOutputs()) > 0 || r.GetStream() != nil,
			len(r.GetSegmentOutputs()) > 0 || r.GetSegments() != nil, len(r.GetImageOutputs()) > 0)
	case *livekit.EgressInfo_Web:
		r := request.Web
		text("url", redactURL(r.GetUrl()))
		flag("audioOnly", r.GetAudioOnly())
		flag("videoOnly", r.GetVideoOnly())
		encoding(r.GetPreset(), r.GetAdvanced())
		addOutputs(len(r.GetFileOutputs()) > 0 || r.GetFile() != nil, len(r.GetStreamOutputs()) > 0 || r.GetStream() != nil,
			len(r.GetSegmentOutputs()) > 0 || r.GetSegments() != nil, len(r.GetImageOutputs()) > 0)
	case *livekit.EgressInfo_Participant:
		r := request.Participant
		text("participantIdentity", r.GetIdentity())
		flag("screenShare", r.GetScreenShare())
		encoding(r.GetPreset(), r.GetAdvanced())
		addOutputs(len(r.GetFileOutputs()) > 0, len(r.GetStreamOutputs()) > 0, len(r.GetSegmentOutputs()) > 0,
			len(r.GetImageOutputs()) > 0)
	case *livekit.EgressInfo_TrackComposite:
		r := request.TrackComposite
		text("audioTrackId", r.GetAudioTrackId())
		text("videoTrackId", r.GetVideoTrackId())
		encoding(r.GetPreset(), r.GetAdvanced())
		addOutputs(len(r.GetFileOutputs()) > 0 || r.GetFile() != nil, len(r.GetStreamOutputs()) > 0 || r.GetStream() != nil,
			len(r.GetSegmentOutputs()) > 0 || r.GetSegments() != nil, len(r.GetImageOutputs()) > 0)
	case *livekit.EgressInfo_Track:
		r := request.Track
		text("trackId", r.GetTrackId())
		addOutputs(r.GetFile() != nil, r.GetWebsocketUrl() != "", false, false)
	case *livekit.EgressInfo_Egress:
		r := request.Egress
		unifiedSource(summary, r.GetTemplate(), r.GetWeb(), r.GetMedia())
		encoding(r.GetPreset(), r.GetAdvanced())
		for _, output := range r.GetOutputs() {
			addOutputs(output.GetFile() != nil, output.GetStream() != nil, output.GetSegments() != nil, output.GetImages() != nil)
		}
	case *livekit.EgressInfo_Replay:
		r := request.Replay
		unifiedSource(summary, r.GetTemplate(), r.GetWeb(), r.GetMedia())
		encoding(r.GetPreset(), r.GetAdvanced())
		for _, output := range r.GetOutputs() {
			addOutputs(output.GetFile() != nil, output.GetStream() != nil, output.GetSegments() != nil, output.GetImages() != nil)
		}
	}
	if len(outputs) > 0 {
		kinds := bson.A{}
		for _, kind := range outputs {
			kinds = append(kinds, kind)
		}
		summary.set("outputs", kinds)
	}
	return summary.doc()
}

// egressRawRequest is the request as raw events store egress.request: the set member of the oneof under its name,
// allow-listed fields only.
func egressRawRequest(info *livekit.EgressInfo, encode schema.EncodeOptions) bson.D {
	for _, element := range schema.Encode(info, encode) {
		if element.Key == "request" {
			request, _ := element.Value.(bson.D)
			return request
		}
	}
	return nil
}

func unifiedSource(summary *fields, template *livekit.TemplateSource, web *livekit.WebSource, media *livekit.MediaSource) {
	switch {
	case template != nil:
		if template.GetLayout() != "" {
			summary.set("layout", template.GetLayout())
		}
		if template.GetCustomBaseUrl() != "" {
			summary.set("customBaseUrl", schema.RedactURL(template.GetCustomBaseUrl()))
		}
		if template.GetAudioOnly() {
			summary.set("audioOnly", true)
		}
		if template.GetVideoOnly() {
			summary.set("videoOnly", true)
		}
	case web != nil:
		if web.GetUrl() != "" {
			summary.set("url", schema.RedactURL(web.GetUrl()))
		}
		if web.GetAudioOnly() {
			summary.set("audioOnly", true)
		}
		if web.GetVideoOnly() {
			summary.set("videoOnly", true)
		}
	case media != nil:
		if media.GetVideoTrackId() != "" {
			summary.set("videoTrackId", media.GetVideoTrackId())
		}
		if participant := media.GetParticipantVideo(); participant != nil {
			summary.set("participantIdentity", participant.GetIdentity())
			if participant.GetPreferScreenShare() {
				summary.set("screenShare", true)
			}
		}
	}
}

// redactURL redacts a non-empty URL and keeps an empty one empty.
func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	return schema.RedactURL(raw)
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// egressOutputs lists the results of an egress: files, streams (URL redacted), segment playlists and image series.
func egressOutputs(info *livekit.EgressInfo) bson.A {
	outputs := bson.A{}
	times := func(f *fields, started int64, ended int64, duration int64) {
		if started != 0 {
			f.set("startedAt", schema.UnixNanos(started))
		}
		if ended != 0 {
			f.set("endedAt", schema.UnixNanos(ended))
		}
		if duration != 0 {
			f.set("durationMs", schema.NanosToMs(duration))
		}
	}
	for _, file := range info.GetFileResults() {
		f := newFields().set("kind", "file")
		if file.GetFilename() != "" {
			f.set("filename", file.GetFilename())
		}
		if file.GetLocation() != "" {
			f.set("location", file.GetLocation())
		}
		if file.GetSize() != 0 {
			f.set("size", file.GetSize())
		}
		times(f, file.GetStartedAt(), file.GetEndedAt(), file.GetDuration())
		outputs = append(outputs, f.doc())
	}
	for _, stream := range info.GetStreamResults() {
		f := newFields().set("kind", "stream")
		if stream.GetUrl() != "" {
			f.set("url", schema.RedactURL(stream.GetUrl()))
		}
		f.set("status", stream.GetStatus().String())
		if stream.GetError() != "" {
			f.set("error", stream.GetError())
		}
		times(f, stream.GetStartedAt(), stream.GetEndedAt(), stream.GetDuration())
		outputs = append(outputs, f.doc())
	}
	for _, segments := range info.GetSegmentResults() {
		f := newFields().set("kind", "segments")
		if segments.GetPlaylistName() != "" {
			f.set("playlistName", segments.GetPlaylistName())
		}
		if segments.GetPlaylistLocation() != "" {
			f.set("location", segments.GetPlaylistLocation())
		}
		if segments.GetSize() != 0 {
			f.set("size", segments.GetSize())
		}
		times(f, segments.GetStartedAt(), segments.GetEndedAt(), segments.GetDuration())
		outputs = append(outputs, f.doc())
	}
	for _, images := range info.GetImageResults() {
		f := newFields().set("kind", "images")
		if images.GetFilenamePrefix() != "" {
			f.set("filenamePrefix", images.GetFilenamePrefix())
		}
		times(f, images.GetStartedAt(), images.GetEndedAt(), 0)
		outputs = append(outputs, f.doc())
	}
	return outputs
}

func deriveEgress(opts Options) *fields {
	retention, _, _ := opts.Retention.Of(schema.RetentionEntities)
	terminal := in(field("status"), egressTerminal)
	f := newFields()
	f.set("outputs", keep("outputs", bson.A{}))
	f.set("retryCount", keep("retryCount", int32(0)))
	f.set("backupStorageUsed", keep("backupStorageUsed", false))
	f.set("durationMs", cond(exists("endedAt"), subtract(field("endedAt"), field("startedAt")), remove))
	f.set("expiresAt", cond(terminal, add(ifNull("endedAt", field("updatedAt")), retention.Milliseconds()), nil))
	return f
}
