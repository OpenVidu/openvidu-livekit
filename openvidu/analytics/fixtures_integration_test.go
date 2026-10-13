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

//go:build integration

package analytics

import (
	"context"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/testutil"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
)

// TestRecordedFixturesReplay writes every recorded event and stats flush with validators in error mode: real
// server output must pass the contract and leave none of its credentials or metadata stored.
func TestRecordedFixturesReplay(t *testing.T) {
	client := testutil.StartMongo(t)
	ctx := context.Background()
	env := newEnv(t, client, func(c *openviduconfig.AnalyticsConfig) { c.StatsDetail = "track" })
	fixtures := loadEventFixtures(t)
	for start := 0; start < len(fixtures); start += 10 {
		var batch []*livekit.AnalyticsEvent
		for _, fixture := range fixtures[start:min(start+10, len(fixtures))] {
			batch = append(batch, fixture.event)
		}
		env.apply(batch)
	}
	for _, stats := range loadStatsFixtures(t) {
		env.apply(nil, stats.GetStats())
	}

	var lifecycle schema.RoomDocument
	require.NoError(t, env.db.Collection(schema.CollectionRooms).FindOne(ctx, bson.D{{Key: "name", Value: "fx-lifecycle"}}).Decode(&lifecycle))
	require.Equal(t, "closed", lifecycle.Status)
	require.Equal(t, "SERVER_SHUTDOWN", lifecycle.EndReason)
	require.Equal(t, int64(2), count(env, schema.CollectionIngressSessions, bson.D{}))
	require.Equal(t, int64(1), count(env, schema.CollectionTrackSubscriptions, bson.D{{Key: "status", Value: "failed"}}))
	require.Equal(t, int64(2), count(env, schema.CollectionEgresses, bson.D{{Key: "status", Value: "complete"}}))
	require.NotZero(t, count(env, schema.CollectionTrackStats, bson.D{}))
	assertNoSecrets(t, env)

	secrets := map[string]bool{}
	for _, fixture := range fixtures {
		event := fixture.event
		for _, secret := range []string{event.GetRoom().GetTurnPassword(), event.GetIngress().GetStreamKey(),
			event.GetRoom().GetMetadata(), event.GetParticipant().GetMetadata()} {
			if secret != "" {
				secrets[secret] = true
			}
		}
	}
	require.NotEmpty(t, secrets)
	for collection, documents := range snapshot(env, allCollections...) {
		for _, document := range documents {
			encoded, err := bson.MarshalExtJSON(document, false, false)
			require.NoError(t, err)
			for secret := range secrets {
				require.NotContains(t, string(encoded), secret, collection)
			}
		}
	}
}
