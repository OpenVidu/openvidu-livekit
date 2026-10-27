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

package schema

import "google.golang.org/protobuf/reflect/protoreflect"

type fieldRule uint8

const (
	// keep copies the field.
	keep fieldRule = iota
	// redactURL keeps scheme, host and path with the userinfo, the query and the last path segment removed.
	redactURL
	// stripURLCredentials keeps scheme, host and path with the userinfo and the query removed.
	stripURLCredentials
	// metadata is copied only with EncodeOptions.StoreMetadata.
	metadata
	// clientAddress is dropped with EncodeOptions.AnonymizeIP.
	clientAddress
)

// allowList names, per protocol message, the only fields that may be stored. Anything else, present or added to the
// protocol later, is dropped: credentials (Room.turn_password, IngressInfo.stream_key, storage uploads, webhook
// signing keys, SIP URIs) are never listed. A message-typed field is copied only when its message is listed too.
//
// The AnalyticsEvent entry leaves out the keys a raw event lifts to its top level (id, type, timestamp, room_id,
// participant_id, track_id, egress_id, ingress_id, node_id), so encoding an event yields its stored payload.
var allowList = map[protoreflect.FullName]map[protoreflect.Name]fieldRule{
	"livekit.AnalyticsEvent": {
		"room": keep, "participant": keep, "track": keep, "client_info": keep, "client_meta": keep,
		"max_subscribed_video_quality": keep, "publisher": keep, "mime": keep, "egress": keep, "ingress": keep,
		"error": keep, "rtp_stats": keep, "video_layer": keep, "sip_call_id": keep, "sip_call": keep,
		"room_end_reason": keep,
	},
	"livekit.Room": {
		"sid": keep, "name": keep, "empty_timeout": keep, "departure_timeout": keep, "max_participants": keep,
		"creation_time": keep, "creation_time_ms": keep, "enabled_codecs": keep, "metadata": metadata,
		"num_participants": keep, "num_publishers": keep, "active_recording": keep, "version": keep,
	},
	"livekit.Codec":        {"mime": keep, "fmtp_line": keep},
	"livekit.TimedVersion": {"unix_micro": keep, "ticks": keep},
	"livekit.ParticipantInfo": {
		"sid": keep, "identity": keep, "state": keep, "tracks": keep, "metadata": metadata, "joined_at": keep,
		"joined_at_ms": keep, "name": keep, "version": keep, "permission": keep, "region": keep,
		"is_publisher": keep, "kind": keep, "attributes": metadata, "disconnect_reason": keep, "kind_details": keep,
		"client_protocol": keep, "capabilities": keep,
	},
	"livekit.ParticipantPermission": {
		"can_subscribe": keep, "can_publish": keep, "can_publish_data": keep, "can_publish_sources": keep,
		"hidden": keep, "can_update_metadata": keep, "can_subscribe_metrics": keep, "can_manage_agent_session": keep,
	},
	"livekit.TrackInfo": {
		"sid": keep, "type": keep, "name": keep, "muted": keep, "width": keep, "height": keep, "simulcast": keep,
		"source": keep, "layers": keep, "mime_type": keep, "mid": keep, "codecs": keep, "encryption": keep,
		"stream": keep, "version": keep, "audio_features": keep,
	},
	"livekit.SimulcastCodecInfo": {"mime_type": keep, "mid": keep, "cid": keep, "layers": keep, "video_layer_mode": keep},
	"livekit.VideoLayer": {
		"quality": keep, "width": keep, "height": keep, "bitrate": keep, "ssrc": keep, "spatial_layer": keep,
		"rid": keep,
	},
	"livekit.ClientInfo": {
		"sdk": keep, "version": keep, "protocol": keep, "os": keep, "os_version": keep, "device_model": keep,
		"browser": keep, "browser_version": keep, "address": clientAddress, "network": keep, "other_sdks": keep,
		"client_protocol": keep,
	},
	"livekit.AnalyticsClientMeta": {
		"region": keep, "node": keep, "client_addr": clientAddress, "client_connect_time": keep,
		"connection_type": keep, "reconnect_reason": keep, "geo_hash": keep, "country": keep, "isp_asn": keep,
	},
	"livekit.RTPStats": {
		"start_time": keep, "end_time": keep, "duration": keep, "packets": keep, "packet_rate": keep, "bytes": keep,
		"header_bytes": keep, "bitrate": keep, "packets_lost": keep, "packet_loss_rate": keep,
		"packet_loss_percentage": keep, "packets_duplicate": keep, "packet_duplicate_rate": keep,
		"bytes_duplicate": keep, "header_bytes_duplicate": keep, "bitrate_duplicate": keep, "packets_padding": keep,
		"packet_padding_rate": keep, "bytes_padding": keep, "header_bytes_padding": keep, "bitrate_padding": keep,
		"packets_out_of_order": keep, "frames": keep, "frame_rate": keep, "jitter_current": keep, "jitter_max": keep,
		"gap_histogram": keep, "nacks": keep, "nack_acks": keep, "nack_misses": keep, "nack_repeated": keep,
		"plis": keep, "last_pli": keep, "firs": keep, "last_fir": keep, "rtt_current": keep, "rtt_max": keep,
		"key_frames": keep, "last_key_frame": keep, "layer_lock_plis": keep, "last_layer_lock_pli": keep,
	},
	"livekit.EgressInfo": {
		"egress_id": keep, "room_id": keep, "room_name": keep, "source_type": keep, "status": keep, "started_at": keep,
		"ended_at": keep, "updated_at": keep, "egress": keep, "replay": keep, "room_composite": keep, "web": keep,
		"participant": keep, "track_composite": keep, "track": keep, "stream_results": keep, "file_results": keep,
		"segment_results": keep, "image_results": keep, "error": keep, "error_code": keep, "details": keep,
		"manifest_location": keep, "backup_storage_used": keep, "retry_count": keep,
	},
	"livekit.RoomCompositeEgressRequest": {
		"room_name": keep, "layout": keep, "audio_only": keep, "audio_mixing": keep, "video_only": keep,
		"custom_base_url": redactURL, "file": keep, "stream": keep, "segments": keep, "preset": keep, "advanced": keep,
		"file_outputs": keep, "stream_outputs": keep, "segment_outputs": keep, "image_outputs": keep,
	},
	"livekit.WebEgressRequest": {
		"url": redactURL, "audio_only": keep, "video_only": keep, "await_start_signal": keep, "file": keep,
		"stream": keep, "segments": keep, "preset": keep, "advanced": keep, "file_outputs": keep,
		"stream_outputs": keep, "segment_outputs": keep, "image_outputs": keep,
	},
	"livekit.ParticipantEgressRequest": {
		"room_name": keep, "identity": keep, "screen_share": keep, "preset": keep, "advanced": keep,
		"file_outputs": keep, "stream_outputs": keep, "segment_outputs": keep, "image_outputs": keep,
	},
	"livekit.TrackCompositeEgressRequest": {
		"room_name": keep, "audio_track_id": keep, "video_track_id": keep, "file": keep, "stream": keep,
		"segments": keep, "preset": keep, "advanced": keep, "file_outputs": keep, "stream_outputs": keep,
		"segment_outputs": keep, "image_outputs": keep,
	},
	"livekit.TrackEgressRequest": {"room_name": keep, "track_id": keep, "file": keep, "websocket_url": redactURL},
	"livekit.StartEgressRequest": {
		"room_name": keep, "template": keep, "web": keep, "media": keep, "preset": keep, "advanced": keep,
		"outputs": keep,
	},
	"livekit.ExportReplayRequest": {
		"replay_id": keep, "start_offset_ms": keep, "end_offset_ms": keep, "template": keep, "web": keep,
		"media": keep, "preset": keep, "advanced": keep, "outputs": keep,
	},
	"livekit.TemplateSource":      {"layout": keep, "audio_only": keep, "video_only": keep, "custom_base_url": redactURL},
	"livekit.WebSource":           {"url": redactURL, "audio_only": keep, "video_only": keep, "await_start_signal": keep},
	"livekit.MediaSource":         {"video_track_id": keep, "participant_video": keep, "audio": keep},
	"livekit.ParticipantVideo":    {"identity": keep, "prefer_screen_share": keep},
	"livekit.AudioConfig":         {"capture_all": keep, "routes": keep},
	"livekit.AudioRoute":          {"track_id": keep, "participant_identity": keep, "participant_kind": keep, "channel": keep},
	"livekit.Output":              {"file": keep, "stream": keep, "segments": keep, "images": keep},
	"livekit.FileOutput":          {"file_type": keep, "filepath": keep, "disable_manifest": keep},
	"livekit.EncodedFileOutput":   {"file_type": keep, "filepath": keep, "disable_manifest": keep},
	"livekit.DirectFileOutput":    {"filepath": keep, "disable_manifest": keep},
	"livekit.SegmentedFileOutput": {"protocol": keep, "filename_prefix": keep, "playlist_name": keep, "live_playlist_name": keep, "segment_duration": keep, "filename_suffix": keep, "disable_manifest": keep},
	"livekit.StreamOutput":        {"protocol": keep, "urls": redactURL},
	"livekit.ImageOutput": {
		"capture_interval": keep, "width": keep, "height": keep, "filename_prefix": keep, "filename_suffix": keep,
		"image_codec": keep, "disable_manifest": keep,
	},
	"livekit.EncodingOptions": {
		"width": keep, "height": keep, "depth": keep, "framerate": keep, "audio_codec": keep, "audio_bitrate": keep,
		"audio_frequency": keep, "video_codec": keep, "video_bitrate": keep, "key_frame_interval": keep,
	},
	"livekit.StreamInfo": {
		"url": redactURL, "started_at": keep, "ended_at": keep, "duration": keep, "status": keep, "error": keep,
		"last_retry_at": keep, "retries": keep,
	},
	"livekit.FileInfo": {"filename": keep, "started_at": keep, "ended_at": keep, "duration": keep, "size": keep, "location": keep},
	"livekit.SegmentsInfo": {
		"playlist_name": keep, "live_playlist_name": keep, "duration": keep, "size": keep, "playlist_location": keep,
		"live_playlist_location": keep, "segment_count": keep, "started_at": keep, "ended_at": keep,
	},
	"livekit.ImagesInfo": {"filename_prefix": keep, "image_count": keep, "started_at": keep, "ended_at": keep},
	"livekit.IngressInfo": {
		"ingress_id": keep, "name": keep, "url": stripURLCredentials, "input_type": keep, "enable_transcoding": keep,
		"audio": keep, "video": keep, "room_name": keep, "participant_identity": keep, "participant_name": keep,
		"participant_metadata": metadata, "reusable": keep, "state": keep, "enabled": keep,
	},
	"livekit.IngressAudioOptions":         {"name": keep, "source": keep, "preset": keep, "options": keep},
	"livekit.IngressVideoOptions":         {"name": keep, "source": keep, "preset": keep, "options": keep},
	"livekit.IngressAudioEncodingOptions": {"audio_codec": keep, "bitrate": keep, "disable_dtx": keep, "channels": keep},
	"livekit.IngressVideoEncodingOptions": {"video_codec": keep, "frame_rate": keep, "layers": keep},
	"livekit.IngressState": {
		"status": keep, "error": keep, "video": keep, "audio": keep, "room_id": keep, "started_at": keep,
		"ended_at": keep, "updated_at": keep, "resource_id": keep, "tracks": keep,
	},
	"livekit.InputVideoState": {"mime_type": keep, "average_bitrate": keep, "width": keep, "height": keep, "framerate": keep},
	"livekit.InputAudioState": {"mime_type": keep, "average_bitrate": keep, "channels": keep, "sample_rate": keep},
	"livekit.SIPCallInfo": {
		"call_id": keep, "trunk_id": keep, "dispatch_rule_id": keep, "region": keep, "room_name": keep,
		"room_id": keep, "participant_identity": keep, "call_direction": keep, "call_status": keep,
		"created_at_ns": keep, "started_at_ns": keep, "ended_at_ns": keep, "disconnect_reason": keep, "error": keep,
		"call_status_code": keep, "audio_codec": keep, "media_encryption": keep, "sip_call_id": keep,
	},
	"livekit.SIPStatus": {"code": keep, "status": keep},
}
