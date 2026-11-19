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
	"context"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// networkError is a transient error as the driver reports a connection reset.
var networkError = mongo.CommandError{Message: "connection reset by peer", Labels: []string{"NetworkError"}}

// collectionStore records the rollup increments and updates of each call by collection, and fails the first write of
// one collection with a network error.
type collectionStore struct {
	*fakeStore
	t           *testing.T
	failIn      string
	failed      bool
	attempts    map[string]int
	increments  map[string][][]projection.Increment
	updates     map[string][][]projection.Update
	closedRooms []map[string]projection.ClosedRoom
	lookups     int
}

func newCollectionStore(t *testing.T, failIn string, closedRooms ...map[string]projection.ClosedRoom) *collectionStore {
	return &collectionStore{fakeStore: &fakeStore{}, t: t, failIn: failIn, attempts: map[string]int{},
		increments: map[string][][]projection.Increment{}, updates: map[string][][]projection.Update{},
		closedRooms: closedRooms}
}

// collectionOf returns the only collection of a call; a call spanning several collections fails the test.
func collectionOf[T any](t *testing.T, items []T, collection func(T) string) string {
	t.Helper()
	require.NotEmpty(t, items)
	name := collection(items[0])
	for _, item := range items {
		require.Equal(t, name, collection(item), "one collection per call")
	}
	return name
}

func (s *collectionStore) write(collection string) error {
	s.attempts[collection]++
	if collection == s.failIn && !s.failed {
		s.failed = true
		return networkError
	}
	return nil
}

func (s *collectionStore) ApplyIncrements(_ context.Context, increments []projection.Increment) error {
	collection := collectionOf(s.t, increments, func(i projection.Increment) string { return i.Collection })
	if err := s.write(collection); err != nil {
		return err
	}
	s.increments[collection] = append(s.increments[collection], increments)
	return nil
}

func (s *collectionStore) ApplyUpdates(_ context.Context, updates []projection.Update) error {
	collection := collectionOf(s.t, updates, func(u projection.Update) string { return u.Collection })
	if err := s.write(collection); err != nil {
		return err
	}
	s.updates[collection] = append(s.updates[collection], updates)
	return nil
}

func (s *collectionStore) ClosedRooms(context.Context, []string) (map[string]projection.ClosedRoom, error) {
	lookup := s.closedRooms[min(s.lookups, len(s.closedRooms)-1)]
	s.lookups++
	return lookup, nil
}

// counter sums a counter of the written increments of a collection.
func (s *collectionStore) counter(collection string, path string) float64 {
	var total float64
	for _, call := range s.increments[collection] {
		for _, increment := range call {
			switch value := increment.Counters[path].(type) {
			case int32:
				total += float64(value)
			case int64:
				total += float64(value)
			case float64:
				total += value
			}
		}
	}
	return total
}

func newTestProcessor(t *testing.T, s batchStore) *processor {
	return &processor{store: s, opts: projectionOptions(testSettings(t, nil)), cache: newRefCache()}
}

func TestBatchRetriesPerCollection(t *testing.T) {
	ctx := context.Background()

	t.Run("Rollup step interrupted between collections", func(t *testing.T) {
		s := newCollectionStore(t, schema.CollectionAnalyticsHourly,
			map[string]projection.ClosedRoom{"RM_b": {Sessions: 3, PeakActive: 2, Topology: projection.TopologyOneToOne}},
			map[string]projection.ClosedRoom{"RM_b": {Sessions: 7, PeakActive: 7, Topology: projection.TopologyManyToMany}})
		p := newTestProcessor(t, s)
		var ev events
		entries := []*eventEntry{
			{event: ev.roomCreated("RM_a", "a", at("0s"))},
			{event: ev.roomCreated("RM_b", "b", at("1s"))},
			{event: ev.roomEnded("RM_b", "b", at("1s"), at("10m"), livekit.RoomEndReason_ROOM_END_API_DELETE)},
		}

		require.True(t, mongo.IsNetworkError(p.apply(ctx, entries, nil)))
		require.Equal(t, 1, s.attempts[schema.CollectionAnalytics5Min])
		require.Equal(t, 1, s.attempts[schema.CollectionAnalyticsHourly])
		require.Zero(t, s.attempts[schema.CollectionAnalyticsDaily])
		require.Zero(t, p.takeHostedRooms(), "no room is hosted before its rollups are written")
		for _, entry := range entries {
			require.False(t, entry.done())
		}

		require.NoError(t, p.apply(ctx, entries, nil))
		for _, entry := range entries {
			require.True(t, entry.done())
		}
		require.Equal(t, 1, s.attempts[schema.CollectionAnalytics5Min], "the 5-minute collection is not written again")
		require.Equal(t, 2, s.attempts[schema.CollectionAnalyticsHourly])
		require.Equal(t, 1, s.attempts[schema.CollectionAnalyticsDaily])
		require.Equal(t, 2.0, s.counter(schema.CollectionAnalytics5Min, "rooms.created"))
		for _, collection := range []string{schema.CollectionAnalyticsHourly, schema.CollectionAnalyticsDaily} {
			require.Equal(t, 2.0, s.counter(collection, "rooms.created"), collection)
			require.Equal(t, 1.0, s.counter(collection, "rooms.ended"), collection)
			require.Equal(t, 3.0, s.counter(collection, "rooms.participantsSum"), collection)
			require.Equal(t, 1.0, s.counter(collection, "rooms.byTopology."+projection.TopologyOneToOne), collection)
			require.Zero(t, s.counter(collection, "rooms.byTopology."+projection.TopologyManyToMany), collection)
		}
		require.Equal(t, 1, s.lookups, "the room-end increments are computed once")
		require.EqualValues(t, 2, p.takeHostedRooms(), "each ROOM_CREATED is counted once")

		require.NoError(t, p.apply(ctx, entries, nil))
		require.Zero(t, p.takeHostedRooms())
		require.Equal(t, 1, s.attempts[schema.CollectionAnalyticsDaily], "a finished entry writes nothing")
	})

	t.Run("Stats deltas interrupted between collections", func(t *testing.T) {
		s := newCollectionStore(t, schema.CollectionRooms)
		p := newTestProcessor(t, s)
		entries := []*statsEntry{
			{stats: []*livekit.AnalyticsStat{stat("RM_a", "PA_a", "TR_a", livekit.StreamType_UPSTREAM, at("1m"), 4, 100)}},
			{stats: []*livekit.AnalyticsStat{stat("RM_a", "PA_b", "TR_b", livekit.StreamType_DOWNSTREAM, at("1m1s"), 3, 50)}},
		}

		require.True(t, mongo.IsNetworkError(p.apply(ctx, nil, entries)))
		require.Equal(t, 2, s.stats, "track_stats samples inserted")
		require.Len(t, s.updates[schema.CollectionParticipants], 1)
		require.Equal(t, 1, s.attempts[schema.CollectionRooms])
		for _, collection := range rollupCollections {
			require.Zero(t, s.attempts[collection], collection)
		}

		require.NoError(t, p.apply(ctx, nil, entries))
		for _, entry := range entries {
			require.True(t, entry.done())
		}
		require.Equal(t, 2, s.stats, "no track_stats sample is inserted again")
		require.Len(t, s.updates[schema.CollectionParticipants], 1, "the participant deltas are written once")
		require.Len(t, s.updates[schema.CollectionParticipants][0], 2)
		require.Equal(t, 2, s.attempts[schema.CollectionRooms])
		require.Len(t, s.updates[schema.CollectionRooms], 1, "the room deltas are written once")
		require.Len(t, s.updates[schema.CollectionRooms][0], 1)
		for _, collection := range rollupCollections {
			require.Equal(t, 1, s.attempts[collection], collection)
			require.Equal(t, 150.0, s.counter(collection, "bytes.up")+s.counter(collection, "bytes.down"), collection)
		}
	})
}
