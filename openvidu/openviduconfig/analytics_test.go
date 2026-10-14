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
	require.Equal(t, 2160*time.Hour, settings.Retention.Entities)
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
		MongoURL:      "mongodb://localhost:27017",
		Database:      "openvidu",
		Interval:      10 * time.Second,
		FixerInterval: 60 * time.Second,
		Retention: RetentionSettings{
			Events:        168 * time.Hour,
			Stats:         168 * time.Hour,
			Tracks:        168 * time.Hour,
			Entities:      2160 * time.Hour,
			RollupsHourly: 9600 * time.Hour,
		},
		StatsDetail:      "participant",
		StoreTypes:       settings.StoreTypes,
		MaxEvents:        20000,
		MaxStats:         50000,
		ValidationAction: "warn",
		Reconciler:       ReconcilerSettings{Enabled: true, MaxClosuresPerRun: 500},
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
	require.Equal(t, ReconcilerSettings{Enabled: false, MaxClosuresPerRun: 50}, settings.Reconciler)

	settings, _, err = parseAnalytics(t, `
openvidu:
  analytics:
    enabled: true
    mongo_url: mongodb://localhost:27017
    reconciler:
      enabled: true
`).Settings()
	require.NoError(t, err)
	require.Equal(t, ReconcilerSettings{Enabled: true, MaxClosuresPerRun: 500}, settings.Reconciler)
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
	require.Equal(t, 768*time.Hour, settings.Retention.Events)
	require.Equal(t, 768*time.Hour, settings.Retention.Stats)
	require.Equal(t, 2160*time.Hour, settings.Retention.Entities)
	require.Equal(t, 9600*time.Hour, settings.Retention.RollupsHourly)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "openvidu.analytics.retention.events")
	require.Contains(t, warnings[0], "openvidu.analytics.retention.stats")

	both := minimal()
	both.Expiration = "768h"
	both.Retention.Events = "24h"
	settings, warnings, err = both.Settings()
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, settings.Retention.Events)
	require.Equal(t, 168*time.Hour, settings.Retention.Stats)
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
		{"fixer below interval", func(c *AnalyticsConfig) { c.FixerInterval = "5s" }, "openvidu.analytics.fixer_interval"},
		{"retention below 1h", func(c *AnalyticsConfig) { c.Retention.Stats = "30m" }, "openvidu.analytics.retention.stats"},
		{"unparsable duration", func(c *AnalyticsConfig) { c.Retention.Events = "a week" }, "openvidu.analytics.retention.events"},
		{"stats detail", func(c *AnalyticsConfig) { c.StatsDetail = "room" }, "openvidu.analytics.stats_detail"},
		{"validation action", func(c *AnalyticsConfig) { c.ValidationAction = "ignore" }, "openvidu.analytics.validation_action"},
		{"unknown event type", func(c *AnalyticsConfig) { c.Events.StoreTypes = "ROOM_CREATED,ROOM_CLOSED" }, "ROOM_CLOSED"},
		{"small event queue", func(c *AnalyticsConfig) { c.Queue.MaxEvents = 999 }, "openvidu.analytics.queue.max_events"},
		{"small stats queue", func(c *AnalyticsConfig) { c.Queue.MaxStats = 10 }, "openvidu.analytics.queue.max_stats"},
		{"negative closures per run", func(c *AnalyticsConfig) { c.Reconciler.MaxClosuresPerRun = -1 },
			"openvidu.analytics.reconciler.max_closures_per_run"},
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

	var root struct {
		OpenVidu OpenViduConfig `yaml:"openvidu"`
	}
	require.Error(t, yaml.Unmarshal([]byte("openvidu:\n  analytics:\n    interval: soon\n"), &root))
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
