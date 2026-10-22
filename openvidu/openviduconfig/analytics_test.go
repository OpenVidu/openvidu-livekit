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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func parseAnalytics(t *testing.T, document string) AnalyticsConfig {
	t.Helper()
	var root struct {
		OpenVidu OpenViduConfig `yaml:"openvidu"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(document))
	decoder.KnownFields(true)
	require.NoError(t, decoder.Decode(&root))
	return root.OpenVidu.Analytics
}

func minimal() AnalyticsConfig {
	return AnalyticsConfig{Enabled: true, MongoUrl: "mongodb://localhost:27017"}
}

// readmeExample returns the first YAML block of README-openvidu.md, the documented configuration example.
func readmeExample(t *testing.T) string {
	readme, err := os.ReadFile("../../README-openvidu.md")
	require.NoError(t, err)
	_, rest, found := strings.Cut(string(readme), "```yaml\n")
	require.True(t, found, "no YAML block in README-openvidu.md")
	example, _, found := strings.Cut(rest, "```")
	require.True(t, found)
	return example
}

func TestExampleConfiguration(t *testing.T) {
	settings, warnings, err := parseAnalytics(t, readmeExample(t)).Settings()
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, 10*time.Second, settings.Interval)
	require.Equal(t, 90*day, settings.Retention.Entities)
	require.Equal(t, 7*day, settings.Retention.Rollups5Min)
	require.Equal(t, 60*time.Second, settings.Reconciler.Interval)
	require.True(t, settings.AnonymizeIP)
	require.Equal(t, []string{"ROOM_CREATED", "ROOM_ENDED", "PARTICIPANT_JOINED", "PARTICIPANT_ACTIVE",
		"PARTICIPANT_LEFT", "PARTICIPANT_CONNECTION_ABORTED", "EGRESS_STARTED", "EGRESS_ENDED"}, settings.StoreTypes)
}

func TestEventTypeListAsCommaSeparatedString(t *testing.T) {
	config := parseAnalytics(t, `
openvidu:
  analytics:
    enabled: true
    mongo_url: mongodb://localhost:27017
    events:
      store_types: "ROOM_CREATED, ROOM_ENDED,API_CALL"
`)
	settings, _, err := config.Settings()
	require.NoError(t, err)
	require.Equal(t, []string{"ROOM_CREATED", "ROOM_ENDED", "API_CALL"}, settings.StoreTypes)
	require.True(t, settings.Stores("ROOM_CREATED"))
	require.False(t, settings.Stores("PARTICIPANT_JOINED"))
	require.False(t, settings.Stores("API_CALL"), "API_CALL is never stored raw")
}

func TestMinimalConfigurationDefaults(t *testing.T) {
	settings, warnings, err := minimal().Settings()
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, AnalyticsSettings{
		MongoURL: "mongodb://localhost:27017",
		Database: "openvidu",
		Interval: 10 * time.Second,
		Retention: RetentionSettings{
			Events:        7 * day,
			Stats:         7 * day,
			Tracks:        7 * day,
			Entities:      90 * day,
			Rollups5Min:   7 * day,
			RollupsHourly: 400 * day,
		},
		StatsDetail:      "participant",
		AnonymizeIP:      true,
		StoreTypes:       settings.StoreTypes,
		MaxEvents:        20000,
		MaxStats:         50000,
		ValidationAction: "warn",
		Reconciler:       ReconcilerSettings{Enabled: true, Interval: 60 * time.Second, MaxClosuresPerRun: 500},
		Migration:        MigrationSettings{Enabled: true, Rate: 2000, BatchSize: 500},
	}, settings)

	require.Len(t, settings.StoreTypes, len(StorableEventTypes)-3)
	for _, excluded := range []string{"API_CALL", "TRACK_PUBLISHED_UPDATE", "TRACK_MAX_SUBSCRIBED_VIDEO_QUALITY",
		"TRACK_SUBSCRIBE_REQUESTED"} {
		require.NotContains(t, settings.StoreTypes, excluded)
	}
	require.Contains(t, settings.StoreTypes, "INGRESS_UPDATED")
}

func TestReconcilerSettings(t *testing.T) {
	settings, _, err := parseAnalytics(t, `
openvidu:
  analytics:
    enabled: true
    mongo_url: mongodb://localhost:27017
    reconciler:
      enabled: false
      max_closures_per_run: 50
`).Settings()
	require.NoError(t, err)
	require.Equal(t, ReconcilerSettings{Enabled: false, Interval: 60 * time.Second, MaxClosuresPerRun: 50},
		settings.Reconciler)

	settings, _, err = parseAnalytics(t, `
openvidu:
  analytics:
    enabled: true
    mongo_url: mongodb://localhost:27017
    reconciler:
      enabled: true
      interval: 30s
`).Settings()
	require.NoError(t, err)
	require.Equal(t, ReconcilerSettings{Enabled: true, Interval: 30 * time.Second, MaxClosuresPerRun: 500},
		settings.Reconciler)
}

func TestMigrationSettings(t *testing.T) {
	settings, _, err := parseAnalytics(t, `
openvidu:
  analytics:
    enabled: true
    mongo_url: mongodb://localhost:27017
    migration:
      enabled: false
      rate: 0
      batch_size: 100
`).Settings()
	require.NoError(t, err)
	require.Equal(t, MigrationSettings{Enabled: false, Rate: 0, BatchSize: 100}, settings.Migration)
}

func TestStatsDetailDefaultFollowsTheEdition(t *testing.T) {
	previous := DefaultStatsDetail
	t.Cleanup(func() { DefaultStatsDetail = previous })

	DefaultStatsDetail = "track"
	settings, _, err := minimal().Settings()
	require.NoError(t, err)
	require.Equal(t, "track", settings.StatsDetail)

	explicit := minimal()
	explicit.StatsDetail = "participant"
	settings, _, err = explicit.Settings()
	require.NoError(t, err)
	require.Equal(t, "participant", settings.StatsDetail)
}

func TestDeprecatedExpiration(t *testing.T) {
	onlyExpiration := minimal()
	onlyExpiration.Expiration = "768h"
	settings, warnings, err := onlyExpiration.Settings()
	require.NoError(t, err)
	require.Equal(t, 32*day, settings.Retention.Events)
	require.Equal(t, 32*day, settings.Retention.Stats)
	require.Equal(t, 90*day, settings.Retention.Entities)
	require.Equal(t, 7*day, settings.Retention.Rollups5Min)
	require.Equal(t, 400*day, settings.Retention.RollupsHourly)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "openvidu.analytics.retention.events")
	require.Contains(t, warnings[0], "openvidu.analytics.retention.stats")

	both := minimal()
	both.Expiration = "768h"
	both.Retention.Events = "1d"
	settings, warnings, err = both.Settings()
	require.NoError(t, err)
	require.Equal(t, day, settings.Retention.Events)
	require.Equal(t, 7*day, settings.Retention.Stats)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "ignored")
}

func TestInvalidValuesNameTheKey(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*AnalyticsConfig)
		key    string
	}{
		{"empty mongo_url", func(c *AnalyticsConfig) { c.MongoUrl = " " }, "openvidu.analytics.mongo_url"},
		{"interval too short", func(c *AnalyticsConfig) { c.Interval = "500ms" }, "openvidu.analytics.interval"},
		{"interval too long", func(c *AnalyticsConfig) { c.Interval = "2m" }, "openvidu.analytics.interval"},
		{"reconciler interval below interval", func(c *AnalyticsConfig) { c.Reconciler.Interval = "5s" },
			"openvidu.analytics.reconciler.interval (5s) must not be shorter than openvidu.analytics.interval (10s)"},
		{"fixer below interval", func(c *AnalyticsConfig) { c.FixerInterval = "5s" }, "openvidu.analytics.fixer_interval"},
		{"retention below 1h", func(c *AnalyticsConfig) { c.Retention.Stats = "30m" }, "openvidu.analytics.retention.stats"},
		{"5-minute rollups below 1h", func(c *AnalyticsConfig) { c.Retention.Rollups5Min = "30m" },
			"openvidu.analytics.retention.rollups_5min must be at least 1h"},
		{"fraction of a second", func(c *AnalyticsConfig) { c.Retention.Events = "168h500ms" },
			"openvidu.analytics.retention.events must be a whole number of seconds"},
		{"retention above the TTL limit", func(c *AnalyticsConfig) { c.Retention.RollupsHourly = "30000d" },
			"openvidu.analytics.retention.rollups_hourly must be at most 24855d3h14m7s (2^31-1 seconds, the MongoDB TTL limit)"},
		{"daily rollups above the TTL limit", func(c *AnalyticsConfig) { c.Retention.RollupsDaily = "30000d" },
			"openvidu.analytics.retention.rollups_daily must be at most"},
		{"entities below 2d", func(c *AnalyticsConfig) { c.Retention.Entities = "1d" },
			"openvidu.analytics.retention.entities must be at least 2d, got 1d"},
		{"daily rollups shorter than hourly", func(c *AnalyticsConfig) { c.Retention.RollupsDaily = "365d" },
			"openvidu.analytics.retention.rollups_daily must not be shorter than openvidu.analytics.retention.rollups_hourly (400d)"},
		{"unparsable duration", func(c *AnalyticsConfig) { c.Retention.Events = "a week" }, "openvidu.analytics.retention.events"},
		{"weeks", func(c *AnalyticsConfig) { c.Retention.Events = "1w" }, "openvidu.analytics.retention.events"},
		{"stats detail", func(c *AnalyticsConfig) { c.StatsDetail = "room" }, "openvidu.analytics.stats_detail"},
		{"validation action", func(c *AnalyticsConfig) { c.ValidationAction = "ignore" }, "openvidu.analytics.validation_action"},
		{"unknown event type", func(c *AnalyticsConfig) { c.Events.StoreTypes = "ROOM_CREATED,ROOM_CLOSED" }, "ROOM_CLOSED"},
		{"small event queue", func(c *AnalyticsConfig) { c.Queue.MaxEvents = 999 }, "openvidu.analytics.queue.max_events"},
		{"small stats queue", func(c *AnalyticsConfig) { c.Queue.MaxStats = 10 }, "openvidu.analytics.queue.max_stats"},
		{"negative closures per run", func(c *AnalyticsConfig) { c.Reconciler.MaxClosuresPerRun = -1 },
			"openvidu.analytics.reconciler.max_closures_per_run"},
		{"negative migration rate", func(c *AnalyticsConfig) { rate := -1; c.Migration.Rate = &rate },
			"openvidu.analytics.migration.rate"},
		{"huge migration batch", func(c *AnalyticsConfig) { c.Migration.BatchSize = 20000 },
			"openvidu.analytics.migration.batch_size"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := minimal()
			test.modify(&config)
			_, _, err := config.Settings()
			require.ErrorContains(t, err, test.key)
		})
	}
}

func TestDurationYAML(t *testing.T) {
	config := parseAnalytics(t, "openvidu:\n  analytics:\n    interval: 10000000000\n")
	interval, err := config.Interval.Value()
	require.NoError(t, err)
	require.Equal(t, 10*time.Second, interval, "a bare integer keeps meaning nanoseconds")

	config = parseAnalytics(t, "openvidu:\n  analytics:\n    enabled: true\n    mongo_url: mongodb://localhost\n    interval: soon\n")
	_, _, err = config.Settings()
	require.ErrorContains(t, err, `openvidu.analytics.interval: "soon" is not a duration: use a Go duration such as 10s `+
		`or 168h, or whole days such as 7d or 1d12h`)

	var root struct {
		OpenVidu OpenViduConfig `yaml:"openvidu"`
	}
	require.Error(t, yaml.Unmarshal([]byte("openvidu:\n  analytics:\n    interval: [10s]\n"), &root))
}

func TestDurationSyntax(t *testing.T) {
	valid := map[string]time.Duration{
		"7d":          7 * day,
		"400d":        400 * day,
		"1d12h":       36 * time.Hour,
		"2d1h30m":     49*time.Hour + 30*time.Minute,
		"168h":        168 * time.Hour,
		"1.5h":        90 * time.Minute,
		"90m":         90 * time.Minute,
		"10000000000": 10 * time.Second,
		" 7d ":        7 * day,
	}
	for text, expected := range valid {
		value, err := Duration(text).Value()
		require.NoError(t, err, text)
		require.Equal(t, expected, value, text)
	}
	for _, text := range []string{"1w", "1y", "1.5d", "12h1d", "1d-1h", "1d+1h", "d", "7 d", "106752d"} {
		_, err := Duration(text).Value()
		require.Error(t, err, text)
	}
}

func TestFormatDuration(t *testing.T) {
	for expected, value := range map[string]time.Duration{
		"90d":           90 * day,
		"1d12h":         36 * time.Hour,
		"10s":           10 * time.Second,
		"1m":            time.Minute,
		"1h30m":         90 * time.Minute,
		"12h":           12 * time.Hour,
		"7d500ms":       7*day + 500*time.Millisecond,
		"24855d3h14m7s": maxRetention,
		"0s":            0,
	} {
		require.Equal(t, expected, FormatDuration(value))
		if value > 0 {
			parsed, err := Duration(expected).Value()
			require.NoError(t, err)
			require.Equal(t, value, parsed, "FormatDuration output parses back")
		}
	}
}

func TestDailyRollupsRetention(t *testing.T) {
	for _, value := range []Duration{"", "0"} {
		config := minimal()
		config.Retention.RollupsDaily = value
		settings, _, err := config.Settings()
		require.NoError(t, err)
		require.Zero(t, settings.Retention.RollupsDaily, "absent or 0 keeps daily rollups forever")
	}

	config := minimal()
	config.Retention.RollupsDaily = "1825d"
	settings, _, err := config.Settings()
	require.NoError(t, err)
	require.Equal(t, 1825*day, settings.Retention.RollupsDaily)
}

func TestRetentionLimitsAccepted(t *testing.T) {
	config := minimal()
	config.Retention.Entities = "2d"
	config.Retention.RollupsHourly = "24855d"
	config.Retention.RollupsDaily = "24855d"
	settings, _, err := config.Settings()
	require.NoError(t, err)
	require.Equal(t, 2*day, settings.Retention.Entities)
	require.Equal(t, 24855*day, settings.Retention.RollupsDaily)
}

func TestDeprecatedFixerInterval(t *testing.T) {
	onlyAlias := minimal()
	onlyAlias.FixerInterval = "30s"
	settings, warnings, err := onlyAlias.Settings()
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, settings.Reconciler.Interval)
	require.Equal(t, []string{"openvidu.analytics.fixer_interval is deprecated: its value now sets " +
		"openvidu.analytics.reconciler.interval; use that key instead"}, warnings)

	both := minimal()
	both.FixerInterval = "30s"
	both.Reconciler.Interval = "20s"
	settings, warnings, err = both.Settings()
	require.NoError(t, err)
	require.Equal(t, 20*time.Second, settings.Reconciler.Interval)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "ignored")
}

func TestAnonymizeIPDefault(t *testing.T) {
	settings, _, err := minimal().Settings()
	require.NoError(t, err)
	require.True(t, settings.AnonymizeIP)

	stored := false
	config := minimal()
	config.AnonymizeIP = &stored
	settings, _, err = config.Settings()
	require.NoError(t, err)
	require.False(t, settings.AnonymizeIP)

	settings, _, err = parseAnalytics(t, `
openvidu:
  analytics:
    enabled: true
    mongo_url: mongodb://localhost:27017
    anonymize_ip: false
`).Settings()
	require.NoError(t, err)
	require.False(t, settings.AnonymizeIP)
}

func TestLogFieldsInConfigurationSyntax(t *testing.T) {
	config := minimal()
	config.Retention.Tracks = "36h"
	settings, _, err := config.Settings()
	require.NoError(t, err)
	fields := map[string]any{}
	logFields := settings.LogFields()
	for i := 0; i < len(logFields); i += 2 {
		fields[logFields[i].(string)] = logFields[i+1]
	}
	require.Equal(t, "90d", fields["retention.entities"])
	require.Equal(t, "1d12h", fields["retention.tracks"])
	require.Equal(t, "7d", fields["retention.rollups_5min"])
	require.Equal(t, "forever", fields["retention.rollups_daily"])
	require.Equal(t, "10s", fields["interval"])
	require.Equal(t, "1m", fields["reconciler.interval"])
	require.NotContains(t, fields, "fixer_interval")
}

func TestRedactMongoURL(t *testing.T) {
	require.Equal(t, "mongodb://admin:***@mongo:27017/?replicaSet=rs0",
		RedactMongoURL("mongodb://admin:secret@mongo:27017/?replicaSet=rs0"))
	require.Equal(t, "mongodb://admin:***@h1:27017,h2:27017/openvidu",
		RedactMongoURL("mongodb://admin:s@cret@h1:27017,h2:27017/openvidu"))
	require.Equal(t, "mongodb://mongo:27017/?replicaSet=rs0", RedactMongoURL("mongodb://mongo:27017/?replicaSet=rs0"))

	settings, _, err := AnalyticsConfig{MongoUrl: "mongodb://admin:secret@mongo:27017/?replicaSet=rs0"}.Settings()
	require.NoError(t, err)
	line := fmt.Sprint(settings.LogFields()...)
	require.Contains(t, line, "mongodb://admin:***@mongo:27017/?replicaSet=rs0")
	require.NotContains(t, line, "secret")
}
