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

package store

import (
	"context"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

const (
	// defaultBatchInterval is the batch interval of a node document written before it recorded one: the writer's
	// default.
	defaultBatchInterval = 10 * time.Second
	// maxBatchInterval is the longest batch interval the configuration accepts.
	maxBatchInterval = time.Minute
)

// SilentWindow is how long a node with batchInterval may go without a heartbeat and still be alive: the silent window
// of the dashboard.
func SilentWindow(batchInterval time.Duration) time.Duration {
	return max(time.Minute, 2*batchInterval+15*time.Second)
}

// Aggregation is an aggregation the aggregator runs, exposed so that tests can check its plan.
type Aggregation struct {
	Collection string
	Pipeline   mongo.Pipeline
	Hint       string
}

// ActiveRoomsAggregation counts the rooms with status active per media node label.
func ActiveRoomsAggregation() Aggregation {
	return Aggregation{Collection: schema.CollectionRooms, Hint: "status_lastEventAt_active",
		Pipeline: countByMediaNode(bson.D{{Key: "status", Value: "active"}})}
}

// ActiveParticipantsAggregation counts the STANDARD participant sessions with status active per media node label.
func ActiveParticipantsAggregation() Aggregation {
	return Aggregation{Collection: schema.CollectionParticipants, Hint: "status_kind",
		Pipeline: countByMediaNode(bson.D{{Key: "status", Value: "active"}, {Key: "kind", Value: projection.StandardKind}})}
}

func countByMediaNode(filter bson.D) mongo.Pipeline {
	return mongo.Pipeline{
		{{Key: "$match", Value: filter}},
		{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$mediaNode"}, {Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}}}}},
	}
}

// ActiveCount is the number of active entities, in total and per media node label; entities without a label count only
// in the total.
type ActiveCount struct {
	Total  int32
	ByNode map[string]int32
}

// ActiveRooms counts the rooms with status active.
func (s *Store) ActiveRooms(ctx context.Context) (ActiveCount, error) {
	return s.countActive(ctx, ActiveRoomsAggregation())
}

// ActiveParticipants counts the STANDARD participant sessions with status active.
func (s *Store) ActiveParticipants(ctx context.Context) (ActiveCount, error) {
	return s.countActive(ctx, ActiveParticipantsAggregation())
}

func (s *Store) countActive(ctx context.Context, aggregation Aggregation) (ActiveCount, error) {
	s.roundTrips++
	cursor, err := s.db.Collection(aggregation.Collection).Aggregate(ctx, aggregation.Pipeline,
		options.Aggregate().SetHint(aggregation.Hint))
	if err != nil {
		return ActiveCount{}, err
	}
	var groups []struct {
		Node *string `bson:"_id"`
		N    int32   `bson:"n"`
	}
	if err := cursor.All(ctx, &groups); err != nil {
		return ActiveCount{}, err
	}
	count := ActiveCount{ByNode: map[string]int32{}}
	for _, group := range groups {
		count.Total += group.N
		if group.Node != nil && *group.Node != "" {
			count.ByNode[*group.Node] += group.N
		}
	}
	return count, nil
}

// LiveMediaNodes returns, sorted, the media node labels of the nodes that have not stopped and whose last heartbeat is
// within their silent window at now.
func (s *Store) LiveMediaNodes(ctx context.Context, now time.Time) ([]string, error) {
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionNodes).Find(ctx, bson.D{
		{Key: "mediaNode", Value: bson.D{{Key: "$exists", Value: true}}},
		{Key: "stoppedAt", Value: bson.D{{Key: "$exists", Value: false}}},
		{Key: "lastSeenAt", Value: bson.D{{Key: "$gte", Value: now.Add(-SilentWindow(maxBatchInterval))}}},
	}, options.Find().SetProjection(bson.D{{Key: "mediaNode", Value: 1}, {Key: "lastSeenAt", Value: 1},
		{Key: "batchIntervalMs", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var nodes []struct {
		MediaNode       string    `bson:"mediaNode"`
		LastSeenAt      time.Time `bson:"lastSeenAt"`
		BatchIntervalMs *int64    `bson:"batchIntervalMs"`
	}
	if err := cursor.All(ctx, &nodes); err != nil {
		return nil, err
	}
	var labels []string
	for _, node := range nodes {
		interval := defaultBatchInterval
		if node.BatchIntervalMs != nil {
			interval = time.Duration(*node.BatchIntervalMs) * time.Millisecond
		}
		if node.MediaNode != "" && now.Sub(node.LastSeenAt) <= SilentWindow(interval) && !slices.Contains(labels, node.MediaNode) {
			labels = append(labels, node.MediaNode)
		}
	}
	slices.Sort(labels)
	return labels, nil
}

// DaysWithoutUniqueIdentities returns, at most limit and the oldest first, the UTC days in [from, to) whose global
// daily rollup lacks uniqueIdentities.
func (s *Store) DaysWithoutUniqueIdentities(ctx context.Context, from time.Time, to time.Time, limit int) ([]time.Time, error) {
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionAnalyticsDaily).Find(ctx, bson.D{
		{Key: "scope", Value: "global"},
		{Key: "bucket", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lt", Value: to}}},
		{Key: "uniqueIdentities", Value: bson.D{{Key: "$exists", Value: false}}},
	}, options.Find().SetSort(bson.D{{Key: "bucket", Value: 1}}).SetLimit(int64(limit)).
		SetProjection(bson.D{{Key: "bucket", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var documents []struct {
		Bucket time.Time `bson:"bucket"`
	}
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, err
	}
	days := make([]time.Time, len(documents))
	for i, document := range documents {
		days[i] = document.Bucket.UTC()
	}
	return days, nil
}

// UniqueIdentitiesPipeline counts the distinct identities of the STANDARD participant sessions that joined in
// [from, to), overall and per media node label.
func UniqueIdentitiesPipeline(from time.Time, to time.Time) mongo.Pipeline {
	return mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "joinedAt", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lt", Value: to}}},
			{Key: "kind", Value: projection.StandardKind},
		}}},
		{{Key: "$facet", Value: bson.D{
			{Key: "global", Value: bson.A{
				bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$identity"}}}},
				bson.D{{Key: "$count", Value: "n"}},
			}},
			{Key: "nodes", Value: bson.A{
				bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: bson.D{{Key: "node", Value: "$mediaNode"},
					{Key: "identity", Value: "$identity"}}}}}},
				bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$_id.node"}, {Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}}}}},
			}},
		}}},
	}
}

// UniqueIdentities counts the distinct identities of the participant sessions that joined in [from, to), overall and
// per media node label; sessions without a label count only overall.
func (s *Store) UniqueIdentities(ctx context.Context, from time.Time, to time.Time) (int32, map[string]int32, error) {
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionParticipants).Aggregate(ctx, UniqueIdentitiesPipeline(from, to))
	if err != nil {
		return 0, nil, err
	}
	var results []struct {
		Global []struct {
			N int32 `bson:"n"`
		} `bson:"global"`
		Nodes []struct {
			Node *string `bson:"_id"`
			N    int32   `bson:"n"`
		} `bson:"nodes"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return 0, nil, err
	}
	perNode := map[string]int32{}
	if len(results) == 0 {
		return 0, perNode, nil
	}
	var global int32
	if len(results[0].Global) > 0 {
		global = results[0].Global[0].N
	}
	for _, node := range results[0].Nodes {
		if node.Node != nil && *node.Node != "" {
			perNode[*node.Node] = node.N
		}
	}
	return global, perNode, nil
}

// SetUniqueIdentities writes uniqueIdentities on the existing daily rollups of day: the per-node values, zero on the
// other node scopes, and the global value last, since its absence marks the day as pending.
func (s *Store) SetUniqueIdentities(ctx context.Context, day time.Time, global int32, perNode map[string]int32) error {
	collection := s.db.Collection(schema.CollectionAnalyticsDaily)
	if len(perNode) > 0 {
		var models []mongo.WriteModel
		for _, node := range sortedNodes(perNode) {
			models = append(models, mongo.NewUpdateOneModel().
				SetFilter(bson.D{{Key: "scope", Value: "node:" + node}, {Key: "bucket", Value: day}}).
				SetUpdate(bson.D{{Key: "$set", Value: bson.D{{Key: "uniqueIdentities", Value: perNode[node]}}}}))
		}
		if err := s.bulkWrite(ctx, schema.CollectionAnalyticsDaily, models); err != nil {
			return err
		}
	}
	s.roundTrips++
	if _, err := collection.UpdateMany(ctx, bson.D{
		{Key: "scope", Value: bson.D{{Key: "$gt", Value: "node:"}, {Key: "$lt", Value: "node;"}}},
		{Key: "bucket", Value: day},
		{Key: "uniqueIdentities", Value: bson.D{{Key: "$exists", Value: false}}},
	}, bson.D{{Key: "$set", Value: bson.D{{Key: "uniqueIdentities", Value: int32(0)}}}}); err != nil {
		return err
	}
	s.roundTrips++
	_, err := collection.UpdateOne(ctx, bson.D{{Key: "scope", Value: "global"}, {Key: "bucket", Value: day}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "uniqueIdentities", Value: global}}}})
	return err
}

func sortedNodes(perNode map[string]int32) []string {
	nodes := make([]string, 0, len(perNode))
	for node := range perNode {
		nodes = append(nodes, node)
	}
	slices.Sort(nodes)
	return nodes
}
