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

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Document types of the data model. Field types follow the contract: int32 for counters, int64 for milliseconds and
// bytes, float64 for scores and ratios. Optional fields are pointers or omitempty and absent while unknown;
// ExpiresAt is null while an entity is open. Required slices and maps must be non-nil when written.

type ByteTotals struct {
	Up   int64 `bson:"up"`
	Down int64 `bson:"down"`
}

type QualityTotals struct {
	ScoreSum   float64  `bson:"scoreSum"`
	ScoreCount int32    `bson:"scoreCount"`
	MinScore   *float64 `bson:"minScore,omitempty"`
	Poor       int32    `bson:"poor"`
	Lost       int32    `bson:"lost"`
}

type RoomConfig struct {
	MaxParticipants    *int32 `bson:"maxParticipants,omitempty"`
	EmptyTimeoutMs     *int64 `bson:"emptyTimeoutMs,omitempty"`
	DepartureTimeoutMs *int64 `bson:"departureTimeoutMs,omitempty"`
	Metadata           string `bson:"metadata,omitempty"`
}

type RoomParticipantCounters struct {
	Sessions   int32            `bson:"sessions"`
	Unique     int32            `bson:"unique"`
	Active     int32            `bson:"active"`
	PeakActive int32            `bson:"peakActive"`
	Aborted    int32            `bson:"aborted"`
	ByKind     map[string]int32 `bson:"byKind"`
}

type RoomTrackCounters struct {
	Published int32            `bson:"published"`
	BySource  map[string]int32 `bson:"bySource"`
}

type RoomEgressCounters struct {
	Total  int32            `bson:"total"`
	Active int32            `bson:"active"`
	ByType map[string]int32 `bson:"byType"`
}

type RoomIngressCounters struct {
	Sessions int32 `bson:"sessions"`
	Active   int32 `bson:"active"`
}

type RoomFeatures struct {
	Egress  bool `bson:"egress"`
	Ingress bool `bson:"ingress"`
	Agent   bool `bson:"agent"`
	SIP     bool `bson:"sip"`
}

// RoomDocument is a `rooms` document (one room session).
type RoomDocument struct {
	ID            string                  `bson:"_id"`
	Name          string                  `bson:"name"`
	NameLower     string                  `bson:"nameLower"`
	Status        string                  `bson:"status"`
	CreatedAt     time.Time               `bson:"createdAt"`
	EndedAt       *time.Time              `bson:"endedAt,omitempty"`
	DurationMs    *int64                  `bson:"durationMs,omitempty"`
	EndReason     string                  `bson:"endReason,omitempty"`
	ClosedBy      string                  `bson:"closedBy,omitempty"`
	NodeID        string                  `bson:"nodeId"`
	Config        *RoomConfig             `bson:"config,omitempty"`
	Participants  RoomParticipantCounters `bson:"participants"`
	Tracks        RoomTrackCounters       `bson:"tracks"`
	Egress        RoomEgressCounters      `bson:"egress"`
	Ingress       RoomIngressCounters     `bson:"ingress"`
	Bytes         ByteTotals              `bson:"bytes"`
	Quality       QualityTotals           `bson:"quality"`
	Features      RoomFeatures            `bson:"features"`
	LastEventAt   time.Time               `bson:"lastEventAt"`
	LastStatsAt   *time.Time              `bson:"lastStatsAt,omitempty"`
	Internal      bson.M                  `bson:"internal,omitempty"`
	SchemaVersion int32                   `bson:"schemaVersion"`
	ExpiresAt     *time.Time              `bson:"expiresAt"`
}

type ParticipantClient struct {
	SDK            string `bson:"sdk,omitempty"`
	Version        string `bson:"version,omitempty"`
	OS             string `bson:"os,omitempty"`
	OSVersion      string `bson:"osVersion,omitempty"`
	Browser        string `bson:"browser,omitempty"`
	BrowserVersion string `bson:"browserVersion,omitempty"`
	DeviceModel    string `bson:"deviceModel,omitempty"`
	Network        string `bson:"network,omitempty"`
	Address        string `bson:"address,omitempty"`
}

type ParticipantPermission struct {
	CanSubscribe        *bool    `bson:"canSubscribe,omitempty"`
	CanPublish          *bool    `bson:"canPublish,omitempty"`
	CanPublishData      *bool    `bson:"canPublishData,omitempty"`
	CanPublishSources   []string `bson:"canPublishSources,omitempty"`
	Hidden              *bool    `bson:"hidden,omitempty"`
	CanUpdateMetadata   *bool    `bson:"canUpdateMetadata,omitempty"`
	CanSubscribeMetrics *bool    `bson:"canSubscribeMetrics,omitempty"`
}

type ParticipantTrackCounters struct {
	Published         int32            `bson:"published"`
	BySource          map[string]int32 `bson:"bySource"`
	Subscribed        int32            `bson:"subscribed"`
	SubscribeFailures int32            `bson:"subscribeFailures"`
}

type ParticipantQuality struct {
	ScoreSum    float64    `bson:"scoreSum"`
	ScoreCount  int32      `bson:"scoreCount"`
	MinScore    *float64   `bson:"minScore,omitempty"`
	Poor        int32      `bson:"poor"`
	Lost        int32      `bson:"lost"`
	LastScore   *float64   `bson:"lastScore,omitempty"`
	LastScoreAt *time.Time `bson:"lastScoreAt,omitempty"`
}

// ParticipantDocument is a `participants` document (one participant session).
type ParticipantDocument struct {
	ID               string                   `bson:"_id"`
	Identity         string                   `bson:"identity"`
	IdentityLower    string                   `bson:"identityLower"`
	Name             string                   `bson:"name,omitempty"`
	RoomSID          string                   `bson:"roomSid"`
	RoomName         string                   `bson:"roomName"`
	Kind             string                   `bson:"kind"`
	KindDetails      []string                 `bson:"kindDetails,omitempty"`
	Status           string                   `bson:"status"`
	JoinedAt         time.Time                `bson:"joinedAt"`
	ActiveAt         *time.Time               `bson:"activeAt,omitempty"`
	LeftAt           *time.Time               `bson:"leftAt,omitempty"`
	DurationMs       *int64                   `bson:"durationMs,omitempty"`
	DisconnectReason string                   `bson:"disconnectReason,omitempty"`
	ClosedBy         string                   `bson:"closedBy,omitempty"`
	ConnectTimeMs    *int32                   `bson:"connectTimeMs,omitempty"`
	ConnectionType   string                   `bson:"connectionType,omitempty"`
	Region           string                   `bson:"region,omitempty"`
	NodeID           string                   `bson:"nodeId"`
	Resumes          int32                    `bson:"resumes"`
	Client           *ParticipantClient       `bson:"client,omitempty"`
	Permission       *ParticipantPermission   `bson:"permission,omitempty"`
	IsPublisher      bool                     `bson:"isPublisher"`
	Metadata         string                   `bson:"metadata,omitempty"`
	Attributes       map[string]string        `bson:"attributes,omitempty"`
	Tracks           ParticipantTrackCounters `bson:"tracks"`
	Bytes            ByteTotals               `bson:"bytes"`
	Quality          ParticipantQuality       `bson:"quality"`
	LastEventAt      time.Time                `bson:"lastEventAt"`
	LastStatsAt      *time.Time               `bson:"lastStatsAt,omitempty"`
	Internal         bson.M                   `bson:"internal,omitempty"`
	SchemaVersion    int32                    `bson:"schemaVersion"`
	ExpiresAt        *time.Time               `bson:"expiresAt"`
}

type TrackCodecLayer struct {
	Quality string `bson:"quality"`
	Width   *int32 `bson:"width,omitempty"`
	Height  *int32 `bson:"height,omitempty"`
	Bitrate *int32 `bson:"bitrate,omitempty"`
}

type TrackCodec struct {
	MimeType string            `bson:"mimeType"`
	Layers   []TrackCodecLayer `bson:"layers,omitempty"`
}

type RtpLayerStats struct {
	Layer   int32 `bson:"layer"`
	Bytes   int64 `bson:"bytes"`
	Packets int32 `bson:"packets"`
	Frames  int32 `bson:"frames"`
}

// RtpFinalStats holds the final RTP statistics of a closed publication or subscription.
type RtpFinalStats struct {
	Bytes         int64   `bson:"bytes"`
	Packets       int32   `bson:"packets"`
	PacketsLost   int32   `bson:"packetsLost"`
	PacketLossPct float64 `bson:"packetLossPct"`
	Nacks         int32   `bson:"nacks"`
	Plis          int32   `bson:"plis"`
	Firs          int32   `bson:"firs"`
	RttMaxMs      int32   `bson:"rttMaxMs"`
	JitterMaxMs   float64 `bson:"jitterMaxMs"`
	DurationMs    int64   `bson:"durationMs"`
}

type TrackPublishStats struct {
	RtpFinalStats `bson:",inline"`
	Layers        []RtpLayerStats `bson:"layers"`
}

type TrackSubscriberCounters struct {
	Current int32 `bson:"current"`
	Total   int32 `bson:"total"`
	Failed  int32 `bson:"failed"`
}

// TrackDocument is a `tracks` document (one published track).
type TrackDocument struct {
	ID             string                  `bson:"_id"`
	RoomSID        string                  `bson:"roomSid"`
	ParticipantSID string                  `bson:"participantSid"`
	Identity       string                  `bson:"identity,omitempty"`
	Type           string                  `bson:"type"`
	Source         string                  `bson:"source"`
	MimeType       string                  `bson:"mimeType,omitempty"`
	Name           string                  `bson:"name,omitempty"`
	Status         string                  `bson:"status"`
	PublishedAt    time.Time               `bson:"publishedAt"`
	UnpublishedAt  *time.Time              `bson:"unpublishedAt,omitempty"`
	DurationMs     *int64                  `bson:"durationMs,omitempty"`
	ClosedBy       string                  `bson:"closedBy,omitempty"`
	Muted          bool                    `bson:"muted"`
	Width          *int32                  `bson:"width,omitempty"`
	Height         *int32                  `bson:"height,omitempty"`
	Simulcast      *bool                   `bson:"simulcast,omitempty"`
	Codecs         []TrackCodec            `bson:"codecs,omitempty"`
	Encryption     string                  `bson:"encryption,omitempty"`
	Subscribers    TrackSubscriberCounters `bson:"subscribers"`
	PublishStats   *TrackPublishStats      `bson:"publishStats,omitempty"`
	Internal       bson.M                  `bson:"internal,omitempty"`
	SchemaVersion  int32                   `bson:"schemaVersion"`
	ExpiresAt      *time.Time              `bson:"expiresAt"`
}

// TrackSubscriptionDocument is a `track_subscriptions` document (one track and subscriber).
type TrackSubscriptionDocument struct {
	ID                 string         `bson:"_id"`
	TrackSID           string         `bson:"trackSid"`
	RoomSID            string         `bson:"roomSid"`
	SubscriberSID      string         `bson:"subscriberSid"`
	SubscriberIdentity string         `bson:"subscriberIdentity,omitempty"`
	PublisherSID       string         `bson:"publisherSid,omitempty"`
	PublisherIdentity  string         `bson:"publisherIdentity,omitempty"`
	Source             string         `bson:"source,omitempty"`
	Status             string         `bson:"status"`
	Error              string         `bson:"error,omitempty"`
	SubscribedAt       *time.Time     `bson:"subscribedAt,omitempty"`
	UnsubscribedAt     *time.Time     `bson:"unsubscribedAt,omitempty"`
	DurationMs         *int64         `bson:"durationMs,omitempty"`
	ClosedBy           string         `bson:"closedBy,omitempty"`
	MaxVideoQuality    string         `bson:"maxVideoQuality,omitempty"`
	SubscribeStats     *RtpFinalStats `bson:"subscribeStats,omitempty"`
	SchemaVersion      int32          `bson:"schemaVersion"`
	ExpiresAt          *time.Time     `bson:"expiresAt"`
}

// EgressRequestSummary is the sanitized egress request: no credentials, no stream keys, redacted URLs.
type EgressRequestSummary struct {
	Layout              string   `bson:"layout,omitempty"`
	CustomBaseURL       string   `bson:"customBaseUrl,omitempty"`
	URL                 string   `bson:"url,omitempty"`
	AudioOnly           *bool    `bson:"audioOnly,omitempty"`
	VideoOnly           *bool    `bson:"videoOnly,omitempty"`
	ParticipantIdentity string   `bson:"participantIdentity,omitempty"`
	ScreenShare         *bool    `bson:"screenShare,omitempty"`
	TrackID             string   `bson:"trackId,omitempty"`
	AudioTrackID        string   `bson:"audioTrackId,omitempty"`
	VideoTrackID        string   `bson:"videoTrackId,omitempty"`
	Preset              string   `bson:"preset,omitempty"`
	Advanced            bson.D   `bson:"advanced,omitempty"`
	Outputs             []string `bson:"outputs,omitempty"`
}

type EgressOutput struct {
	Kind           string     `bson:"kind"`
	Filename       string     `bson:"filename,omitempty"`
	URL            string     `bson:"url,omitempty"`
	PlaylistName   string     `bson:"playlistName,omitempty"`
	FilenamePrefix string     `bson:"filenamePrefix,omitempty"`
	Location       string     `bson:"location,omitempty"`
	Size           *int64     `bson:"size,omitempty"`
	DurationMs     *int64     `bson:"durationMs,omitempty"`
	Status         string     `bson:"status,omitempty"`
	Error          string     `bson:"error,omitempty"`
	StartedAt      *time.Time `bson:"startedAt,omitempty"`
	EndedAt        *time.Time `bson:"endedAt,omitempty"`
}

type EgressStatusChange struct {
	Status string    `bson:"status"`
	At     time.Time `bson:"at"`
}

// EgressDocument is an `egresses` document.
type EgressDocument struct {
	ID                string               `bson:"_id"`
	RoomSID           string               `bson:"roomSid,omitempty"`
	RoomName          string               `bson:"roomName,omitempty"`
	Type              string               `bson:"type"`
	SourceType        string               `bson:"sourceType"`
	Status            string               `bson:"status"`
	ClosedBy          string               `bson:"closedBy,omitempty"`
	StartedAt         time.Time            `bson:"startedAt"`
	UpdatedAt         time.Time            `bson:"updatedAt"`
	EndedAt           *time.Time           `bson:"endedAt,omitempty"`
	DurationMs        *int64               `bson:"durationMs,omitempty"`
	Error             string               `bson:"error,omitempty"`
	ErrorCode         *int32               `bson:"errorCode,omitempty"`
	Request           EgressRequestSummary `bson:"request"`
	Outputs           []EgressOutput       `bson:"outputs"`
	StatusHistory     []EgressStatusChange `bson:"statusHistory"`
	RetryCount        int32                `bson:"retryCount"`
	BackupStorageUsed bool                 `bson:"backupStorageUsed"`
	SchemaVersion     int32                `bson:"schemaVersion"`
	ExpiresAt         *time.Time           `bson:"expiresAt"`
}

// IngressDocument is an `ingresses` document (one ingress definition).
type IngressDocument struct {
	ID                  string     `bson:"_id"`
	Name                string     `bson:"name,omitempty"`
	NameLower           string     `bson:"nameLower,omitempty"`
	InputType           string     `bson:"inputType"`
	URL                 string     `bson:"url,omitempty"`
	RoomName            string     `bson:"roomName"`
	ParticipantIdentity string     `bson:"participantIdentity"`
	ParticipantName     string     `bson:"participantName,omitempty"`
	ParticipantMetadata string     `bson:"participantMetadata,omitempty"`
	Reusable            bool       `bson:"reusable"`
	Enabled             bool       `bson:"enabled"`
	EnableTranscoding   *bool      `bson:"enableTranscoding,omitempty"`
	CreatedAt           time.Time  `bson:"createdAt"`
	DeletedAt           *time.Time `bson:"deletedAt,omitempty"`
	Status              string     `bson:"status"`
	LastError           string     `bson:"lastError,omitempty"`
	Sessions            int32      `bson:"sessions"`
	TotalDurationMs     int64      `bson:"totalDurationMs"`
	LastSessionAt       *time.Time `bson:"lastSessionAt,omitempty"`
	Internal            bson.M     `bson:"internal,omitempty"`
	SchemaVersion       int32      `bson:"schemaVersion"`
	ExpiresAt           *time.Time `bson:"expiresAt"`
}

type IngressVideoInfo struct {
	MimeType       string   `bson:"mimeType,omitempty"`
	AverageBitrate *int32   `bson:"averageBitrate,omitempty"`
	Width          *int32   `bson:"width,omitempty"`
	Height         *int32   `bson:"height,omitempty"`
	Framerate      *float64 `bson:"framerate,omitempty"`
}

type IngressAudioInfo struct {
	MimeType       string `bson:"mimeType,omitempty"`
	AverageBitrate *int32 `bson:"averageBitrate,omitempty"`
	Channels       *int32 `bson:"channels,omitempty"`
	SampleRate     *int32 `bson:"sampleRate,omitempty"`
}

// IngressSessionDocument is an `ingress_sessions` document (one publishing session).
type IngressSessionDocument struct {
	ID                  string            `bson:"_id"`
	IngressID           string            `bson:"ingressId"`
	IngressName         string            `bson:"ingressName,omitempty"`
	InputType           string            `bson:"inputType"`
	RoomSID             string            `bson:"roomSid,omitempty"`
	RoomName            string            `bson:"roomName,omitempty"`
	ParticipantIdentity string            `bson:"participantIdentity,omitempty"`
	Status              string            `bson:"status"`
	Error               string            `bson:"error,omitempty"`
	ClosedBy            string            `bson:"closedBy,omitempty"`
	StartedAt           time.Time         `bson:"startedAt"`
	EndedAt             *time.Time        `bson:"endedAt,omitempty"`
	DurationMs          *int64            `bson:"durationMs,omitempty"`
	Video               *IngressVideoInfo `bson:"video,omitempty"`
	Audio               *IngressAudioInfo `bson:"audio,omitempty"`
	SchemaVersion       int32             `bson:"schemaVersion"`
	ExpiresAt           *time.Time        `bson:"expiresAt"`
}

// EventDocument is an `events` document (one raw analytics event).
type EventDocument struct {
	ID               string    `bson:"_id"`
	Type             string    `bson:"type"`
	TS               time.Time `bson:"ts"`
	RoomSID          string    `bson:"roomSid,omitempty"`
	RoomName         string    `bson:"roomName,omitempty"`
	ParticipantSID   string    `bson:"participantSid,omitempty"`
	Identity         string    `bson:"identity,omitempty"`
	TrackSID         string    `bson:"trackSid,omitempty"`
	EgressID         string    `bson:"egressId,omitempty"`
	IngressID        string    `bson:"ingressId,omitempty"`
	IngressSessionID string    `bson:"ingressSessionId,omitempty"`
	NodeID           string    `bson:"nodeId"`
	Synthetic        bool      `bson:"synthetic"`
	Payload          bson.D    `bson:"payload"`
}

type TrackStatsMeta struct {
	RoomSID        string `bson:"roomSid"`
	ParticipantSID string `bson:"participantSid"`
	TrackSID       string `bson:"trackSid,omitempty"`
	Kind           string `bson:"kind"`
	NodeID         string `bson:"nodeId"`
}

// TrackStatsDocument is a `track_stats` document (one 30-second flush).
type TrackStatsDocument struct {
	ID          *bson.ObjectID  `bson:"_id,omitempty"`
	TS          time.Time       `bson:"ts"`
	Meta        TrackStatsMeta  `bson:"meta"`
	Score       float64         `bson:"score"`
	MinScore    float64         `bson:"minScore"`
	MedianScore float64         `bson:"medianScore"`
	Bytes       int64           `bson:"bytes"`
	Packets     int32           `bson:"packets"`
	PacketsLost int32           `bson:"packetsLost"`
	RttMs       int32           `bson:"rttMs"`
	JitterMs    float64         `bson:"jitterMs"`
	Nacks       int32           `bson:"nacks"`
	Plis        int32           `bson:"plis"`
	Firs        int32           `bson:"firs"`
	Frames      int32           `bson:"frames"`
	Layers      []RtpLayerStats `bson:"layers"`
	MimeType    string          `bson:"mimeType,omitempty"`
}

type RollupBytes struct {
	Up   *int64 `bson:"up,omitempty"`
	Down *int64 `bson:"down,omitempty"`
}

type RollupQuality struct {
	ScoreSum   *float64 `bson:"scoreSum,omitempty"`
	ScoreCount *int32   `bson:"scoreCount,omitempty"`
	Poor       *int32   `bson:"poor,omitempty"`
	Lost       *int32   `bson:"lost,omitempty"`
}

type RollupRooms struct {
	Created         *int32           `bson:"created,omitempty"`
	Ended           *int32           `bson:"ended,omitempty"`
	EndedByReason   map[string]int32 `bson:"endedByReason,omitempty"`
	DurationMsSum   *int64           `bson:"durationMsSum,omitempty"`
	ParticipantsSum *int32           `bson:"participantsSum,omitempty"`
}

type RollupParticipants struct {
	Joined            *int32           `bson:"joined,omitempty"`
	Active            *int32           `bson:"active,omitempty"`
	Aborted           *int32           `bson:"aborted,omitempty"`
	Left              *int32           `bson:"left,omitempty"`
	ByKind            map[string]int32 `bson:"byKind,omitempty"`
	BySDK             map[string]int32 `bson:"bySdk,omitempty"`
	ByOS              map[string]int32 `bson:"byOs,omitempty"`
	ByBrowser         map[string]int32 `bson:"byBrowser,omitempty"`
	ByConnectionType  map[string]int32 `bson:"byConnectionType,omitempty"`
	DisconnectReasons map[string]int32 `bson:"disconnectReasons,omitempty"`
	ConnectTimeSumMs  *int64           `bson:"connectTimeSumMs,omitempty"`
	ConnectTimeCount  *int32           `bson:"connectTimeCount,omitempty"`
	Minutes           *float64         `bson:"minutes,omitempty"`
}

type RollupTracks struct {
	Published       *int32 `bson:"published,omitempty"`
	Subscribed      *int32 `bson:"subscribed,omitempty"`
	SubscribeFailed *int32 `bson:"subscribeFailed,omitempty"`
}

type RollupEgress struct {
	Started       *int32           `bson:"started,omitempty"`
	ByType        map[string]int32 `bson:"byType,omitempty"`
	EndedByStatus map[string]int32 `bson:"endedByStatus,omitempty"`
	DurationMs    *int64           `bson:"durationMs,omitempty"`
}

type RollupIngress struct {
	SessionsStarted *int32 `bson:"sessionsStarted,omitempty"`
	SessionsEnded   *int32 `bson:"sessionsEnded,omitempty"`
	DurationMs      *int64 `bson:"durationMs,omitempty"`
}

type RollupAPICalls struct {
	Total    *int32           `bson:"total,omitempty"`
	ByMethod map[string]int32 `bson:"byMethod,omitempty"`
}

// HourlyRollupDocument is an `analytics_hourly` document. Counters are created lazily by $inc, so all are optional.
type HourlyRollupDocument struct {
	ID           string              `bson:"_id"`
	Scope        string              `bson:"scope"`
	Bucket       time.Time           `bson:"bucket"`
	Rooms        *RollupRooms        `bson:"rooms,omitempty"`
	Participants *RollupParticipants `bson:"participants,omitempty"`
	Tracks       *RollupTracks       `bson:"tracks,omitempty"`
	Egress       *RollupEgress       `bson:"egress,omitempty"`
	Ingress      *RollupIngress      `bson:"ingress,omitempty"`
	Bytes        *RollupBytes        `bson:"bytes,omitempty"`
	Quality      *RollupQuality      `bson:"quality,omitempty"`
	APICalls     *RollupAPICalls     `bson:"apiCalls,omitempty"`
	APIErrors    *RollupAPICalls     `bson:"apiErrors,omitempty"`
}

// DailyRollupDocument is an `analytics_daily` document.
type DailyRollupDocument struct {
	HourlyRollupDocument `bson:",inline"`
	UniqueIdentities     *int32 `bson:"uniqueIdentities,omitempty"`
}

type FiveMinuteRooms struct {
	Created *int32 `bson:"created,omitempty"`
	Ended   *int32 `bson:"ended,omitempty"`
}

type FiveMinuteParticipants struct {
	Connected *int32 `bson:"connected,omitempty"`
	Joined    *int32 `bson:"joined,omitempty"`
	Active    *int32 `bson:"active,omitempty"`
	Left      *int32 `bson:"left,omitempty"`
	Aborted   *int32 `bson:"aborted,omitempty"`
}

type FiveMinuteEgress struct {
	Started *int32 `bson:"started,omitempty"`
	Ended   *int32 `bson:"ended,omitempty"`
}

// FiveMinuteRollupDocument is an `analytics_5min` document.
type FiveMinuteRollupDocument struct {
	ID           string                  `bson:"_id"`
	Scope        string                  `bson:"scope"`
	Bucket       time.Time               `bson:"bucket"`
	Rooms        *FiveMinuteRooms        `bson:"rooms,omitempty"`
	Participants *FiveMinuteParticipants `bson:"participants,omitempty"`
	Egress       *FiveMinuteEgress       `bson:"egress,omitempty"`
	Bytes        *RollupBytes            `bson:"bytes,omitempty"`
	Quality      *RollupQuality          `bson:"quality,omitempty"`
	Internal     bson.M                  `bson:"internal,omitempty"`
}

// NodeDocument is a `nodes` document, refreshed on every writer batch.
type NodeDocument struct {
	ID            string    `bson:"_id"`
	FirstSeenAt   time.Time `bson:"firstSeenAt"`
	LastSeenAt    time.Time `bson:"lastSeenAt"`
	ServerVersion string    `bson:"serverVersion"`
	Region        string    `bson:"region,omitempty"`
}

// Identifiers of the `schema_meta` documents.
const (
	SchemaMetaSchemaID       = "schema"
	SchemaMetaRetentionID    = "retention"
	SchemaMetaCapabilitiesID = "capabilities"
	SchemaMetaConfigID       = "config"
	SchemaMetaMigrationID    = "migration"
)

// SchemaMetaSchema records the contract version implemented by the writer.
type SchemaMetaSchema struct {
	ID            string    `bson:"_id"`
	Version       int32     `bson:"version"`
	WriterVersion string    `bson:"writerVersion"`
	UpdatedAt     time.Time `bson:"updatedAt"`
}

// SchemaMetaRetention records the effective retention per class in milliseconds; a nil RollupsDailyMs keeps daily
// rollups forever.
type SchemaMetaRetention struct {
	ID              string    `bson:"_id"`
	EventsMs        int64     `bson:"eventsMs"`
	StatsMs         int64     `bson:"statsMs"`
	TracksMs        int64     `bson:"tracksMs"`
	EntitiesMs      int64     `bson:"entitiesMs"`
	RollupsHourlyMs int64     `bson:"rollupsHourlyMs"`
	RollupsDailyMs  *int64    `bson:"rollupsDailyMs"`
	UpdatedAt       time.Time `bson:"updatedAt"`
}

// SchemaMetaCapabilities records the MongoDB features detected at initialization.
type SchemaMetaCapabilities struct {
	ID            string    `bson:"_id"`
	Timeseries    bool      `bson:"timeseries"`
	Transactions  bool      `bson:"transactions"`
	ServerVersion string    `bson:"serverVersion"`
	DetectedAt    time.Time `bson:"detectedAt"`
}

// SchemaMetaConfig records writer settings that shape documents.
type SchemaMetaConfig struct {
	ID          string    `bson:"_id"`
	StatsDetail string    `bson:"statsDetail"`
	RecordedAt  time.Time `bson:"recordedAt"`
}

// SchemaMetaMigration records the progress of the legacy data migration.
type SchemaMetaMigration struct {
	ID                  string     `bson:"_id"`
	State               string     `bson:"state"`
	Checkpoint          bson.D     `bson:"checkpoint,omitempty"`
	Processed           int64      `bson:"processed"`
	Total               int64      `bson:"total"`
	StartedAt           *time.Time `bson:"startedAt,omitempty"`
	FinishedAt          *time.Time `bson:"finishedAt,omitempty"`
	Error               string     `bson:"error,omitempty"`
	NodeID              string     `bson:"nodeId,omitempty"`
	LegacyRetainedUntil *time.Time `bson:"legacyRetainedUntil,omitempty"`
}
