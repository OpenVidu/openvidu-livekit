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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

var rollupBucket = time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

func TestRenderIncrement(t *testing.T) {
	setOnInsert := bson.E{Key: "$setOnInsert", Value: bson.D{{Key: "scope", Value: "global"}, {Key: "bucket", Value: rollupBucket}}}

	t.Run("maxima only", func(t *testing.T) {
		increment := Increment{Collection: schema.CollectionAnalytics5Min, Scope: "global", Bucket: rollupBucket,
			Maxima: map[string]any{"rooms.peakActive": int32(3), "participants.peakActive": int32(0)}}
		require.Equal(t, bson.D{setOnInsert, {Key: "$max", Value: bson.D{
			{Key: "participants.peakActive", Value: int32(0)}, {Key: "rooms.peakActive", Value: int32(3)},
		}}}, RenderIncrement(increment))
	})

	t.Run("maxima beside counters and marks", func(t *testing.T) {
		increment := Increment{Collection: schema.CollectionAnalyticsHourly, Scope: "global", Bucket: rollupBucket,
			Counters: map[string]any{"rooms.created": int32(1)}, Maxima: map[string]any{"rooms.peakActive": int32(2)},
			Reported: true}
		require.Equal(t, bson.D{setOnInsert,
			{Key: "$inc", Value: bson.D{{Key: "rooms.created", Value: int32(1)}}},
			{Key: "$max", Value: bson.D{{Key: "rooms.peakActive", Value: int32(2)}, {Key: "mediaStats.reported", Value: true}}},
		}, RenderIncrement(increment))
	})

	t.Run("counters without maxima or marks", func(t *testing.T) {
		increment := Increment{Collection: schema.CollectionAnalyticsHourly, Scope: "global", Bucket: rollupBucket,
			Counters: map[string]any{"rooms.created": int32(1)}}
		require.Equal(t, bson.D{setOnInsert, {Key: "$inc", Value: bson.D{{Key: "rooms.created", Value: int32(1)}}}},
			RenderIncrement(increment))
	})

	t.Run("maxima in a pipeline", func(t *testing.T) {
		increment := Increment{Collection: schema.CollectionAnalytics5Min, Scope: "global", Bucket: rollupBucket,
			Maxima: map[string]any{"rooms.peakActive": int32(4)}, Members: map[string][]string{"internal.connectedSids": {"PA_a"}}}
		pipeline, ok := RenderIncrement(increment).([]bson.D)
		require.True(t, ok)
		fields := pipeline[0][0].Value.(bson.D)
		require.Contains(t, fields, bson.E{Key: "rooms.peakActive", Value: escape(maxOf("rooms.peakActive", int32(4)))})
	})
}

func TestMergeIncrementsKeepsTheLargerMaximum(t *testing.T) {
	increment := func(counters map[string]any, maxima map[string]any) Increment {
		return Increment{Collection: schema.CollectionAnalytics5Min, Scope: "global", Bucket: rollupBucket,
			Counters: counters, Maxima: maxima}
	}
	merged := MergeIncrements([]Increment{
		increment(map[string]any{"rooms.created": int32(1)}, map[string]any{"rooms.peakActive": int32(4)}),
		increment(nil, map[string]any{"rooms.peakActive": int32(2), "participants.peakActive": int32(7)}),
		increment(map[string]any{"rooms.created": int32(1)}, nil),
	})
	require.Len(t, merged, 1)
	require.Equal(t, map[string]any{"rooms.created": int32(2)}, merged[0].Counters)
	require.Equal(t, map[string]any{"rooms.peakActive": int32(4), "participants.peakActive": int32(7)}, merged[0].Maxima)
}

func TestPeakIncrements(t *testing.T) {
	at := rollupBucket.Add(2 * time.Minute)
	maxima := func(increments []Increment) map[string]map[string]any {
		byCollection := map[string]map[string]any{}
		for _, increment := range increments {
			byCollection[increment.Collection] = increment.Maxima
		}
		return byCollection
	}

	t.Run("global", func(t *testing.T) {
		got := maxima(PeakIncrements("global", at, Peaks{Rooms: 3, Participants: 7, Egress: 2, Ingress: 1}))
		require.Equal(t, map[string]any{"rooms.peakActive": int32(3), "participants.peakActive": int32(7),
			"egress.peakActive": int32(2)}, got[schema.CollectionAnalytics5Min])
		for _, collection := range []string{schema.CollectionAnalyticsHourly, schema.CollectionAnalyticsDaily} {
			require.Equal(t, map[string]any{"rooms.peakActive": int32(3), "participants.peakActive": int32(7),
				"egress.peakActive": int32(2), "ingress.peakActive": int32(1)}, got[collection])
		}
	})

	t.Run("node scope without egress or ingress", func(t *testing.T) {
		got := maxima(PeakIncrements("node:mn-1", at, Peaks{Rooms: 1, Participants: 2, Egress: 5, Ingress: 5}))
		for _, collection := range []string{schema.CollectionAnalytics5Min, schema.CollectionAnalyticsHourly,
			schema.CollectionAnalyticsDaily} {
			require.Equal(t, map[string]any{"rooms.peakActive": int32(1), "participants.peakActive": int32(2)}, got[collection])
		}
	})
}
