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

package openviduconfig

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"
	"gopkg.in/yaml.v3"
)

// DefaultStatsDetail is the stats_detail used when the key is absent: "participant" in CE. The PRO server overrides
// it with "track".
var DefaultStatsDetail = "participant"

// AnalyticsConfig is the `openvidu.analytics` section. Durations and the event type list are string kinds so that
// LiveKit derives their LIVEKIT_OPENVIDU_ANALYTICS_* environment variables and CLI flags like any other string.
type AnalyticsConfig struct {
	Enabled       bool     `yaml:"enabled,omitempty"`
	MongoUrl      string   `yaml:"mongo_url,omitempty"`
	Database      string   `yaml:"database,omitempty"`
	Interval      Duration `yaml:"interval,omitempty"`
	FixerInterval Duration `yaml:"fixer_interval,omitempty"`
	// Deprecated: mapped to Retention.Events and Retention.Stats when those are absent.
	Expiration       Duration           `yaml:"expiration,omitempty"`
	Retention        AnalyticsRetention `yaml:"retention,omitempty"`
	StatsDetail      string             `yaml:"stats_detail,omitempty"`
	AnonymizeIP      bool               `yaml:"anonymize_ip,omitempty"`
	StoreMetadata    bool               `yaml:"store_metadata,omitempty"`
	Events           AnalyticsEvents    `yaml:"events,omitempty"`
	Queue            AnalyticsQueue     `yaml:"queue,omitempty"`
	ValidationAction string             `yaml:"validation_action,omitempty"`
}

// AnalyticsRetention holds the retention per data class.
type AnalyticsRetention struct {
	Events        Duration `yaml:"events,omitempty"`
	Stats         Duration `yaml:"stats,omitempty"`
	Tracks        Duration `yaml:"tracks,omitempty"`
	Entities      Duration `yaml:"entities,omitempty"`
	RollupsHourly Duration `yaml:"rollups_hourly,omitempty"`
}

// AnalyticsEvents configures the raw `events` collection.
type AnalyticsEvents struct {
	// StoreTypes lists the event types stored raw; every type is projected whatever this list says.
	StoreTypes EventTypeList `yaml:"store_types,omitempty"`
}

// AnalyticsQueue bounds the in-memory queues of the writer.
type AnalyticsQueue struct {
	MaxEvents int `yaml:"max_events,omitempty"`
	MaxStats  int `yaml:"max_stats,omitempty"`
}

// Duration is a duration in Go syntax ("10s", "168h"). A bare integer is read as nanoseconds, as the former
// time.Duration fields accepted.
type Duration string

// Value parses the duration; the empty string is zero.
func (d Duration) Value() (time.Duration, error) {
	text := strings.TrimSpace(string(d))
	if text == "" {
		return 0, nil
	}
	if nanoseconds, err := strconv.ParseInt(text, 10, 64); err == nil {
		return time.Duration(nanoseconds), nil
	}
	return time.ParseDuration(text)
}

// UnmarshalYAML accepts a duration string or an integer number of nanoseconds and rejects anything else.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: a duration must be a scalar such as 10s or 168h", node.Line)
	}
	candidate := Duration(node.Value)
	if _, err := candidate.Value(); err != nil {
		return fmt.Errorf("line %d: invalid duration %q: %w", node.Line, node.Value, err)
	}
	*d = candidate
	return nil
}

// EventTypeList is a comma-separated list of analytics event type names. In YAML it may also be a sequence.
type EventTypeList string

// UnmarshalYAML accepts a sequence of names or a comma-separated string.
func (l *EventTypeList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*l = EventTypeList(node.Value)
		return nil
	case yaml.SequenceNode:
		names := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: event types must be names", item.Line)
			}
			names = append(names, item.Value)
		}
		*l = EventTypeList(strings.Join(names, ","))
		return nil
	default:
		return fmt.Errorf("line %d: event types must be a list or a comma-separated string", node.Line)
	}
}

// Names returns the trimmed, non-empty names of the list.
func (l EventTypeList) Names() []string {
	var names []string
	for _, name := range strings.Split(string(l), ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// StorableEventTypes are the event types that may be stored raw. API_CALL is accepted in events.store_types but
// never stored.
var StorableEventTypes = []string{
	"ROOM_CREATED", "ROOM_ENDED",
	"PARTICIPANT_JOINED", "PARTICIPANT_ACTIVE", "PARTICIPANT_RESUMED", "PARTICIPANT_LEFT",
	"PARTICIPANT_CONNECTION_ABORTED",
	"TRACK_PUBLISH_REQUESTED", "TRACK_PUBLISHED", "TRACK_PUBLISHED_UPDATE", "TRACK_MUTED", "TRACK_UNMUTED",
	"TRACK_UNPUBLISHED", "TRACK_SUBSCRIBE_REQUESTED", "TRACK_SUBSCRIBED", "TRACK_SUBSCRIBE_FAILED",
	"TRACK_UNSUBSCRIBED", "TRACK_MAX_SUBSCRIBED_VIDEO_QUALITY", "TRACK_PUBLISH_STATS", "TRACK_SUBSCRIBE_STATS",
	"EGRESS_STARTED", "EGRESS_UPDATED", "EGRESS_ENDED",
	"INGRESS_CREATED", "INGRESS_STARTED", "INGRESS_UPDATED", "INGRESS_ENDED", "INGRESS_DELETED",
}

// unstoredByDefault are projected but not stored raw unless events.store_types lists them.
var unstoredByDefault = []string{"TRACK_PUBLISHED_UPDATE", "TRACK_MAX_SUBSCRIBED_VIDEO_QUALITY", "TRACK_SUBSCRIBE_REQUESTED"}

// AnalyticsSettings is the effective analytics configuration: defaults applied, the deprecated key mapped and every
// value validated.
type AnalyticsSettings struct {
	MongoURL         string
	Database         string
	Interval         time.Duration
	FixerInterval    time.Duration
	Retention        RetentionSettings
	StatsDetail      string
	AnonymizeIP      bool
	StoreMetadata    bool
	StoreTypes       []string
	MaxEvents        int
	MaxStats         int
	ValidationAction string
}

// RetentionSettings holds the effective retention per data class.
type RetentionSettings struct {
	Events        time.Duration
	Stats         time.Duration
	Tracks        time.Duration
	Entities      time.Duration
	RollupsHourly time.Duration
}

// Defaults of the analytics configuration.
const (
	DefaultAnalyticsDatabase      = "openvidu"
	DefaultAnalyticsInterval      = 10 * time.Second
	DefaultAnalyticsFixerInterval = 60 * time.Second
	DefaultRetentionEvents        = 168 * time.Hour
	DefaultRetentionStats         = 168 * time.Hour
	DefaultRetentionTracks        = 168 * time.Hour
	DefaultRetentionEntities      = 2160 * time.Hour
	DefaultRetentionRollupsHourly = 9600 * time.Hour
	DefaultQueueMaxEvents         = 20000
	DefaultQueueMaxStats          = 50000
	DefaultValidationAction       = "warn"
)

const keyPrefix = "openvidu.analytics."

// Settings applies the defaults, maps the deprecated expiration key and validates every value. It returns the
// deprecation warnings to log, or an error naming the offending key.
func (c AnalyticsConfig) Settings() (AnalyticsSettings, []string, error) {
	var warnings []string
	settings := AnalyticsSettings{
		MongoURL:         c.MongoUrl,
		Database:         c.Database,
		StatsDetail:      c.StatsDetail,
		AnonymizeIP:      c.AnonymizeIP,
		StoreMetadata:    c.StoreMetadata,
		MaxEvents:        c.Queue.MaxEvents,
		MaxStats:         c.Queue.MaxStats,
		ValidationAction: c.ValidationAction,
	}
	if settings.Database == "" {
		settings.Database = DefaultAnalyticsDatabase
	}
	if settings.StatsDetail == "" {
		settings.StatsDetail = DefaultStatsDetail
	}
	if settings.MaxEvents == 0 {
		settings.MaxEvents = DefaultQueueMaxEvents
	}
	if settings.MaxStats == 0 {
		settings.MaxStats = DefaultQueueMaxStats
	}
	if settings.ValidationAction == "" {
		settings.ValidationAction = DefaultValidationAction
	}

	durations := []struct {
		key      string
		value    Duration
		fallback time.Duration
		target   *time.Duration
	}{
		{"interval", c.Interval, DefaultAnalyticsInterval, &settings.Interval},
		{"fixer_interval", c.FixerInterval, DefaultAnalyticsFixerInterval, &settings.FixerInterval},
		{"retention.events", c.Retention.Events, DefaultRetentionEvents, &settings.Retention.Events},
		{"retention.stats", c.Retention.Stats, DefaultRetentionStats, &settings.Retention.Stats},
		{"retention.tracks", c.Retention.Tracks, DefaultRetentionTracks, &settings.Retention.Tracks},
		{"retention.entities", c.Retention.Entities, DefaultRetentionEntities, &settings.Retention.Entities},
		{"retention.rollups_hourly", c.Retention.RollupsHourly, DefaultRetentionRollupsHourly, &settings.Retention.RollupsHourly},
	}
	for _, d := range durations {
		value, err := d.value.Value()
		if err != nil {
			return settings, nil, fmt.Errorf("%s%s: %w", keyPrefix, d.key, err)
		}
		if value == 0 {
			value = d.fallback
		}
		*d.target = value
	}

	expiration, err := c.Expiration.Value()
	if err != nil {
		return settings, nil, fmt.Errorf("%sexpiration: %w", keyPrefix, err)
	}
	if expiration != 0 {
		if c.Retention.Events == "" && c.Retention.Stats == "" {
			settings.Retention.Events = expiration
			settings.Retention.Stats = expiration
			warnings = append(warnings, fmt.Sprintf("%sexpiration is deprecated: its value now sets %sretention.events "+
				"and %sretention.stats; use those keys instead", keyPrefix, keyPrefix, keyPrefix))
		} else {
			warnings = append(warnings, fmt.Sprintf("%sexpiration is deprecated and ignored because %sretention is set; "+
				"remove it", keyPrefix, keyPrefix))
		}
	}

	if c.Events.StoreTypes == "" {
		for _, name := range StorableEventTypes {
			if !slices.Contains(unstoredByDefault, name) {
				settings.StoreTypes = append(settings.StoreTypes, name)
			}
		}
	} else {
		settings.StoreTypes = c.Events.StoreTypes.Names()
	}

	if err := settings.validate(); err != nil {
		return settings, nil, err
	}
	return settings, warnings, nil
}

func (s AnalyticsSettings) validate() error {
	if strings.TrimSpace(s.MongoURL) == "" {
		return fmt.Errorf("%smongo_url must not be empty", keyPrefix)
	}
	if s.Interval < time.Second || s.Interval > time.Minute {
		return fmt.Errorf("%sinterval must be between 1s and 60s, got %s", keyPrefix, s.Interval)
	}
	if s.FixerInterval < s.Interval {
		return fmt.Errorf("%sfixer_interval (%s) must not be shorter than %sinterval (%s)", keyPrefix, s.FixerInterval,
			keyPrefix, s.Interval)
	}
	retentions := []struct {
		key   string
		value time.Duration
	}{
		{"retention.events", s.Retention.Events},
		{"retention.stats", s.Retention.Stats},
		{"retention.tracks", s.Retention.Tracks},
		{"retention.entities", s.Retention.Entities},
		{"retention.rollups_hourly", s.Retention.RollupsHourly},
	}
	for _, r := range retentions {
		if r.value < time.Hour {
			return fmt.Errorf("%s%s must be at least 1h, got %s", keyPrefix, r.key, r.value)
		}
		if r.value%time.Second != 0 {
			return fmt.Errorf("%s%s must be a whole number of seconds, got %s", keyPrefix, r.key, r.value)
		}
	}
	if s.StatsDetail != "participant" && s.StatsDetail != "track" {
		return fmt.Errorf("%sstats_detail must be participant or track, got %q", keyPrefix, s.StatsDetail)
	}
	if s.ValidationAction != "warn" && s.ValidationAction != "error" {
		return fmt.Errorf("%svalidation_action must be warn or error, got %q", keyPrefix, s.ValidationAction)
	}
	for _, name := range s.StoreTypes {
		if name != livekit.AnalyticsEventType_API_CALL.String() && !slices.Contains(StorableEventTypes, name) {
			return fmt.Errorf("%sevents.store_types: unknown event type %s", keyPrefix, name)
		}
	}
	if s.MaxEvents < 1000 {
		return fmt.Errorf("%squeue.max_events must be at least 1000, got %d", keyPrefix, s.MaxEvents)
	}
	if s.MaxStats < 1000 {
		return fmt.Errorf("%squeue.max_stats must be at least 1000, got %d", keyPrefix, s.MaxStats)
	}
	return nil
}

// Stores reports whether events of the type are stored raw. API_CALL never is.
func (s AnalyticsSettings) Stores(eventType string) bool {
	return eventType != livekit.AnalyticsEventType_API_CALL.String() && slices.Contains(s.StoreTypes, eventType)
}

// LogFields returns the effective configuration as key-value pairs for a log line, with the credentials of the
// MongoDB URL redacted.
func (s AnalyticsSettings) LogFields() []any {
	return []any{
		"mongo_url", RedactMongoURL(s.MongoURL),
		"database", s.Database,
		"interval", s.Interval,
		"fixer_interval", s.FixerInterval,
		"retention.events", s.Retention.Events,
		"retention.stats", s.Retention.Stats,
		"retention.tracks", s.Retention.Tracks,
		"retention.entities", s.Retention.Entities,
		"retention.rollups_hourly", s.Retention.RollupsHourly,
		"stats_detail", s.StatsDetail,
		"anonymize_ip", s.AnonymizeIP,
		"store_metadata", s.StoreMetadata,
		"events.store_types", strings.Join(s.StoreTypes, ","),
		"queue.max_events", s.MaxEvents,
		"queue.max_stats", s.MaxStats,
		"validation_action", s.ValidationAction,
	}
}

// RedactMongoURL replaces the password of a MongoDB connection string with ***. It does not parse the URL, since
// multi-host seed lists (mongodb://u:p@h1:27017,h2:27017/) are not valid for net/url.
func RedactMongoURL(raw string) string {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		return raw
	}
	authority := raw[schemeEnd+3:]
	if end := strings.IndexAny(authority, "/?"); end >= 0 {
		authority = authority[:end]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return raw
	}
	colon := strings.Index(authority[:at], ":")
	if colon < 0 {
		return raw
	}
	start := schemeEnd + 3 + colon + 1
	return raw[:start] + "***" + raw[schemeEnd+3+at:]
}
