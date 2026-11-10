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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// requireGlobal asserts that every increment goes to the global scope, and that some carry a path under prefix.
func requireGlobal(t *testing.T, increments []projection.Increment, prefix string) {
	t.Helper()
	require.NotEmpty(t, increments)
	for _, increment := range increments {
		require.Equal(t, "global", increment.Scope, "%+v", increment)
	}
	var paths []string
	for _, increment := range increments {
		for path := range increment.Counters {
			paths = append(paths, path)
		}
	}
	require.True(t, slices.ContainsFunc(paths, func(path string) bool { return strings.HasPrefix(path, prefix) }), paths)
}

// requireNoMediaNode asserts that no update of collection sets a `mediaNode` field.
func requireNoMediaNode(t *testing.T, updates []projection.Update, collection string) {
	t.Helper()
	for _, update := range updates {
		if update.Collection == collection {
			require.NotContains(t, bson.Raw(mustMarshal(t, update.Pipeline)).String(), `"mediaNode"`)
		}
	}
}

func mustMarshal(t *testing.T, pipeline []bson.D) []byte {
	t.Helper()
	raw, err := bson.Marshal(bson.D{{Key: "pipeline", Value: pipeline}})
	require.NoError(t, err)
	return raw
}

func TestEgressAndIngressCountersAreGlobal(t *testing.T) {
	opts := projectionOptions(testSettings(t, nil))
	opts.MediaNode = "mn-a"
	var ev events

	t.Run("egress counted globally", func(t *testing.T) {
		started := ev.egress(livekit.AnalyticsEventType_EGRESS_STARTED,
			roomCompositeEgress("EG_1", "RM_1", livekit.EgressStatus_EGRESS_STARTING, at("0s"), at("0s")), at("0s"))
		ended := roomCompositeEgress("EG_1", "RM_1", livekit.EgressStatus_EGRESS_COMPLETE, at("0s"), at("60s"))
		ended.EndedAt = at("60s").UnixNano()
		for _, event := range []*livekit.AnalyticsEvent{started, ev.egress(livekit.AnalyticsEventType_EGRESS_ENDED, ended, at("60s"))} {
			result := projection.Event(event, projection.Refs{}, opts)
			requireGlobal(t, result.Increments, "egress.")
			requireNoMediaNode(t, result.Updates, schema.CollectionEgresses)
		}
	})

	t.Run("ingress counted globally", func(t *testing.T) {
		live := ingressState(livekit.IngressState_ENDPOINT_PUBLISHING, "RM_1", "IS_1", at("0s"), time.Time{})
		over := ingressState(livekit.IngressState_ENDPOINT_INACTIVE, "RM_1", "IS_1", at("0s"), at("60s"))
		for _, event := range []*livekit.AnalyticsEvent{
			ev.ingress(livekit.AnalyticsEventType_INGRESS_STARTED, rtmpIngress("IN_1", "room", live), at("0s")),
			ev.ingress(livekit.AnalyticsEventType_INGRESS_ENDED, rtmpIngress("IN_1", "room", over), at("60s")),
		} {
			result := projection.Event(event, projection.Refs{}, opts)
			requireGlobal(t, result.Increments, "ingress.")
			requireNoMediaNode(t, result.Updates, schema.CollectionIngressSessions)
		}
	})

	t.Run("reconciler closures counted globally", func(t *testing.T) {
		egress := projection.CloseEgress(projection.EgressState{ID: "EG_1", RoomSID: "RM_1", Status: "active",
			StartedAt: at("0s")}, at("60s"), reasonRoomClosedNodeDead, opts)
		requireGlobal(t, egress.Increments, "egress.")
		session := projection.CloseIngressSession(projection.IngressSessionState{ID: "IS_1", IngressID: "IN_1",
			RoomSID: "RM_1", Status: "publishing", StartedAt: at("0s")}, at("60s"), reasonRoomClosedNodeDead, opts)
		requireGlobal(t, session.Increments, "ingress.")
	})

	t.Run("rooms keep the node scope", func(t *testing.T) {
		result := projection.Event(ev.roomCreated("RM_1", "room", at("0s")), projection.Refs{}, opts)
		var scopes []string
		for _, increment := range result.Increments {
			scopes = append(scopes, increment.Scope)
		}
		require.Contains(t, scopes, "node:mn-a")
	})
}
