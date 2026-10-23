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
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/protojson"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
)

// emittedTypes are the event types the server emits.
var emittedTypes = []livekit.AnalyticsEventType{
	livekit.AnalyticsEventType_ROOM_CREATED, livekit.AnalyticsEventType_ROOM_ENDED,
	livekit.AnalyticsEventType_PARTICIPANT_JOINED, livekit.AnalyticsEventType_PARTICIPANT_ACTIVE,
	livekit.AnalyticsEventType_PARTICIPANT_RESUMED, livekit.AnalyticsEventType_PARTICIPANT_LEFT,
	livekit.AnalyticsEventType_PARTICIPANT_CONNECTION_ABORTED,
	livekit.AnalyticsEventType_TRACK_PUBLISH_REQUESTED, livekit.AnalyticsEventType_TRACK_PUBLISHED,
	livekit.AnalyticsEventType_TRACK_PUBLISHED_UPDATE, livekit.AnalyticsEventType_TRACK_MUTED,
	livekit.AnalyticsEventType_TRACK_UNMUTED, livekit.AnalyticsEventType_TRACK_UNPUBLISHED,
	livekit.AnalyticsEventType_TRACK_PUBLISH_STATS, livekit.AnalyticsEventType_TRACK_MAX_SUBSCRIBED_VIDEO_QUALITY,
	livekit.AnalyticsEventType_TRACK_SUBSCRIBE_REQUESTED, livekit.AnalyticsEventType_TRACK_SUBSCRIBED,
	livekit.AnalyticsEventType_TRACK_SUBSCRIBE_FAILED, livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED,
	livekit.AnalyticsEventType_TRACK_SUBSCRIBE_STATS,
	livekit.AnalyticsEventType_EGRESS_STARTED, livekit.AnalyticsEventType_EGRESS_UPDATED,
	livekit.AnalyticsEventType_EGRESS_ENDED,
	livekit.AnalyticsEventType_INGRESS_CREATED, livekit.AnalyticsEventType_INGRESS_STARTED,
	livekit.AnalyticsEventType_INGRESS_UPDATED, livekit.AnalyticsEventType_INGRESS_ENDED,
	livekit.AnalyticsEventType_INGRESS_DELETED, livekit.AnalyticsEventType_API_CALL,
}

type eventFixture struct {
	name  string
	event *livekit.AnalyticsEvent
}

// loadEventFixtures reads testdata/events in timestamp order.
func loadEventFixtures(t *testing.T) []eventFixture {
	t.Helper()
	var fixtures []eventFixture
	for _, file := range fixtureFiles(t, "events") {
		event := &livekit.AnalyticsEvent{}
		readFixture(t, file, event)
		fixtures = append(fixtures, eventFixture{name: strings.TrimSuffix(filepath.Base(file), ".json"), event: event})
	}
	sort.SliceStable(fixtures, func(i, j int) bool {
		return fixtures[i].event.GetTimestamp().AsTime().Before(fixtures[j].event.GetTimestamp().AsTime())
	})
	return fixtures
}

func loadStatsFixtures(t *testing.T) map[string]*livekit.AnalyticsStats {
	t.Helper()
	fixtures := map[string]*livekit.AnalyticsStats{}
	for _, file := range fixtureFiles(t, "stats") {
		stats := &livekit.AnalyticsStats{}
		readFixture(t, file, stats)
		fixtures[strings.TrimSuffix(filepath.Base(file), ".json")] = stats
	}
	return fixtures
}

func fixtureFiles(t *testing.T, kind string) []string {
	files, err := filepath.Glob(filepath.Join("testdata", kind, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	return files
}

func readFixture(t *testing.T, file string, message proto.Message) {
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NoError(t, protojson.UnmarshalOptions{}.Unmarshal(data, message), file)
}

func TestFixtureCoverage(t *testing.T) {
	covered := map[livekit.AnalyticsEventType]bool{}
	for _, fixture := range loadEventFixtures(t) {
		require.True(t, strings.HasPrefix(fixture.name, fixture.event.GetType().String()+"_"), fixture.name)
		covered[fixture.event.GetType()] = true
	}
	for _, eventType := range emittedTypes {
		require.True(t, covered[eventType], "no fixture for %s", eventType)
	}
	for value := range livekit.AnalyticsEventType_name {
		eventType := livekit.AnalyticsEventType(value)
		require.Equal(t, slices.Contains(emittedTypes, eventType), projection.Supported(eventType), eventType.String())
	}
}

// familyCollections are the collections each family of events may write; cascades on leave close tracks and
// subscriptions.
var familyCollections = map[string][]string{
	"ROOM":        {schema.CollectionRooms},
	"PARTICIPANT": {schema.CollectionParticipants, schema.CollectionRooms, schema.CollectionTracks, schema.CollectionTrackSubscriptions},
	"PUBLICATION": {schema.CollectionTracks, schema.CollectionParticipants, schema.CollectionRooms},
	"SUBSCRIPTION": {schema.CollectionTrackSubscriptions, schema.CollectionTracks, schema.CollectionParticipants,
		schema.CollectionRooms},
	"EGRESS":   {schema.CollectionEgresses, schema.CollectionRooms},
	"INGRESS":  {schema.CollectionIngresses, schema.CollectionIngressSessions, schema.CollectionRooms},
	"API_CALL": {},
}

func familyOf(eventType livekit.AnalyticsEventType) string {
	name := eventType.String()
	switch {
	case strings.HasPrefix(name, "TRACK_SUBSCRIBE"), name == "TRACK_UNSUBSCRIBED":
		return "SUBSCRIPTION"
	case strings.HasPrefix(name, "TRACK_"):
		return "PUBLICATION"
	case name == "API_CALL":
		return name
	default:
		return strings.SplitN(name, "_", 2)[0]
	}
}

var rollupCollections = []string{schema.CollectionAnalytics5Min, schema.CollectionAnalyticsHourly, schema.CollectionAnalyticsDaily}

func TestEveryEmittedTypeIsProjected(t *testing.T) {
	settings := testSettings(t, func(c *openviduconfig.AnalyticsConfig) { c.StoreMetadata = false })
	opts := projectionOptions(settings)
	for _, fixture := range loadEventFixtures(t) {
		result := projection.Event(fixture.event, projection.Refs{}, opts)
		allowed := familyCollections[familyOf(fixture.event.GetType())]
		for _, update := range append(slices.Clone(result.Updates), result.Cascades...) {
			require.Contains(t, allowed, update.Collection, fixture.name)
		}
		for _, increment := range result.Increments {
			require.Contains(t, rollupCollections, increment.Collection, fixture.name)
		}
		require.Equal(t, settings.Stores(fixture.event.GetType().String()), result.Raw != nil, fixture.name)
		if result.Raw != nil {
			assertNoRecordedSecret(t, fixture, result.Raw)
		}
	}

	var ev events
	unknown := ev.next(livekit.AnalyticsEventType_SIP_CALL_STARTED, at("0s"))
	unknown.RoomId = "RM_x"
	require.Equal(t, projection.EventResult{}, projection.Event(unknown, projection.Refs{}, opts))
}

// assertNoRecordedSecret checks that the credentials and metadata carried by a recorded event stay out of its raw
// document.
func assertNoRecordedSecret(t *testing.T, fixture eventFixture, raw *schema.EventDocument) {
	data, err := bson.MarshalExtJSON(raw, false, false)
	require.NoError(t, err)
	stored := string(data)
	event := fixture.event
	for _, secret := range []string{
		event.GetRoom().GetTurnPassword(), event.GetIngress().GetStreamKey(), event.GetRoom().GetMetadata(),
		event.GetParticipant().GetMetadata(),
	} {
		if secret != "" {
			require.NotContains(t, stored, secret, fixture.name)
		}
	}
	require.NotContains(t, stored, "turn_password", fixture.name)
	require.NotContains(t, stored, "stream_key", fixture.name)
}

func TestRecordedStatsAreProjected(t *testing.T) {
	for _, detail := range []string{"participant", "track"} {
		opts := projectionOptions(testSettings(t, func(c *openviduconfig.AnalyticsConfig) { c.StatsDetail = detail }))
		for name, stats := range loadStatsFixtures(t) {
			result := projection.Stats(stats.GetStats(), opts)
			for _, document := range result.Documents {
				require.NotEmpty(t, document.Meta.RoomSID, name)
				require.NotEmpty(t, document.Meta.ParticipantSID, name)
			}
			for _, update := range result.Updates {
				require.Contains(t, []string{schema.CollectionParticipants, schema.CollectionRooms}, update.Collection, name)
			}
			for _, increment := range result.Increments {
				require.Contains(t, rollupCollections, increment.Collection, name)
			}
		}
	}
}
