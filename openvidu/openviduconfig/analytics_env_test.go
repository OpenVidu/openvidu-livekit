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

package openviduconfig_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/livekit/livekit-server/pkg/config"
)

var analyticsKeys = []string{
	"enabled", "mongo_url", "database", "interval", "fixer_interval", "expiration",
	"retention.events", "retention.stats", "retention.tracks", "retention.entities", "retention.rollups_hourly",
	"stats_detail", "anonymize_ip", "store_metadata", "events.store_types", "queue.max_events", "queue.max_stats",
	"validation_action",
}

func TestEveryAnalyticsKeyHasAnEnvironmentVariable(t *testing.T) {
	flags, err := config.GenerateCLIFlags(nil, true)
	require.NoError(t, err)

	names := map[string]bool{}
	for _, flag := range flags {
		for _, name := range flag.Names() {
			names[name] = true
		}
	}
	for _, key := range analyticsKeys {
		require.True(t, names["openvidu.analytics."+key], "no flag (nor LIVEKIT_OPENVIDU_ANALYTICS_* variable) for %s", key)
	}
}

func TestEnvironmentOverridesYAML(t *testing.T) {
	t.Setenv("LIVEKIT_OPENVIDU_ANALYTICS_RETENTION_EVENTS", "720h")
	t.Setenv("LIVEKIT_OPENVIDU_ANALYTICS_EVENTS_STORE_TYPES", "ROOM_CREATED,ROOM_ENDED")
	t.Setenv("LIVEKIT_OPENVIDU_ANALYTICS_ANONYMIZE_IP", "true")

	flags, err := config.GenerateCLIFlags(nil, true)
	require.NoError(t, err)

	yamlConfig := `
openvidu:
  analytics:
    enabled: true
    mongo_url: mongodb://localhost:27017
    retention:
      events: 168h
`
	var loaded *config.Config
	command := &cli.Command{
		Name:  "test",
		Flags: flags,
		Action: func(_ context.Context, c *cli.Command) error {
			var err error
			loaded, err = config.NewConfig(yamlConfig, true, c, nil)
			return err
		},
	}
	require.NoError(t, command.Run(context.Background(), []string{"test"}))

	settings, _, err := loaded.OpenVidu.Analytics.Settings()
	require.NoError(t, err)
	require.Equal(t, 720*time.Hour, settings.Retention.Events)
	require.Equal(t, []string{"ROOM_CREATED", "ROOM_ENDED"}, settings.StoreTypes)
	require.True(t, settings.AnonymizeIP)
}
