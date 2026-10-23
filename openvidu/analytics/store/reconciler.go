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
	"errors"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// defaultEmptyTimeoutMs is LiveKit's room empty timeout, for rooms whose document lacks its configuration.
const defaultEmptyTimeoutMs = 300_000

// OpenParticipant is an open participant session as the reconciler reads it.
type OpenParticipant struct {
	SID         string     `bson:"_id"`
	RoomSID     string     `bson:"roomSid"`
	RoomName    string     `bson:"roomName"`
	Identity    string     `bson:"identity"`
	Kind        string     `bson:"kind"`
	NodeID      string     `bson:"nodeId"`
	Status      string     `bson:"status"`
	JoinedAt    time.Time  `bson:"joinedAt"`
	ActiveAt    *time.Time `bson:"activeAt"`
	LastStatsAt *time.Time `bson:"lastStatsAt"`
	LastEventAt *time.Time `bson:"lastEventAt"`
}

var openParticipantFields = bson.D{
	{Key: "roomSid", Value: 1}, {Key: "roomName", Value: 1}, {Key: "identity", Value: 1}, {Key: "kind", Value: 1},
	{Key: "nodeId", Value: 1}, {Key: "status", Value: 1}, {Key: "joinedAt", Value: 1}, {Key: "activeAt", Value: 1},
	{Key: "lastStatsAt", Value: 1}, {Key: "lastEventAt", Value: 1},
}

// OpenRoom is an active room as the reconciler reads it.
type OpenRoom struct {
	SID         string     `bson:"_id"`
	Name        string     `bson:"name"`
	NodeID      string     `bson:"nodeId"`
	CreatedAt   time.Time  `bson:"createdAt"`
	LastEventAt time.Time  `bson:"lastEventAt"`
	LastStatsAt *time.Time `bson:"lastStatsAt"`
}

// RoomRef is what the reconciler needs from the room of an egress or an ingress session.
type RoomRef struct {
	SID     string     `bson:"_id"`
	Name    string     `bson:"name"`
	NodeID  string     `bson:"nodeId"`
	Status  string     `bson:"status"`
	EndedAt *time.Time `bson:"endedAt"`
}

// OpenEgress is an egress that has not ended.
type OpenEgress struct {
	ID        string    `bson:"_id"`
	RoomSID   string    `bson:"roomSid"`
	RoomName  string    `bson:"roomName"`
	Status    string    `bson:"status"`
	StartedAt time.Time `bson:"startedAt"`
	UpdatedAt time.Time `bson:"updatedAt"`
}

// OpenIngressSession is an ingress session that has not ended.
type OpenIngressSession struct {
	ID        string    `bson:"_id"`
	IngressID string    `bson:"ingressId"`
	RoomSID   string    `bson:"roomSid"`
	RoomName  string    `bson:"roomName"`
	Status    string    `bson:"status"`
	StartedAt time.Time `bson:"startedAt"`
}

func openParticipantFilter() bson.E {
	return bson.E{Key: "status", Value: bson.D{{Key: "$in", Value: projection.OpenParticipantStatuses}}}
}

// Query is a find the reconciler runs, exposed so that tests can check its plan.
type Query struct {
	Collection string
	Filter     bson.D
	Sort       bson.D
	Limit      int64
}

func (s *Store) find(ctx context.Context, query Query, fields bson.D, results any) error {
	opts := options.Find().SetProjection(fields)
	if query.Sort != nil {
		opts.SetSort(query.Sort)
	}
	if query.Limit > 0 {
		opts.SetLimit(query.Limit)
	}
	s.roundTrips++
	cursor, err := s.db.Collection(query.Collection).Find(ctx, query.Filter, opts)
	if err != nil {
		return err
	}
	return cursor.All(ctx, results)
}

// StaleParticipantQueries are the queries of StaleParticipants: sessions with stats older than before, the stalest
// first, then sessions without stats active (or joined, when never active) before it.
func StaleParticipantQueries(before time.Time, limit int) []Query {
	return []Query{
		{
			Collection: schema.CollectionParticipants,
			Filter:     bson.D{openParticipantFilter(), {Key: "lastStatsAt", Value: bson.D{{Key: "$lt", Value: before}}}},
			Sort:       bson.D{{Key: "lastStatsAt", Value: 1}},
			Limit:      int64(limit),
		},
		{
			Collection: schema.CollectionParticipants,
			Filter: bson.D{
				openParticipantFilter(),
				{Key: "lastStatsAt", Value: nil},
				{Key: "$or", Value: bson.A{
					bson.D{{Key: "activeAt", Value: bson.D{{Key: "$lt", Value: before}}}},
					bson.D{{Key: "activeAt", Value: nil}, {Key: "joinedAt", Value: bson.D{{Key: "$lt", Value: before}}}},
				}},
			},
			Limit: int64(limit),
		},
	}
}

// StaleParticipants returns, at most limit, the open participant sessions without a sign of life since before: a last
// stats flush older than before, or no stats at all and activeAt (joinedAt when never active) older than before.
// Sessions with stats come first, the stalest first.
func (s *Store) StaleParticipants(ctx context.Context, before time.Time, limit int) ([]OpenParticipant, error) {
	var participants []OpenParticipant
	for _, query := range StaleParticipantQueries(before, limit) {
		if len(participants) >= limit {
			break
		}
		query.Limit = int64(limit - len(participants))
		var found []OpenParticipant
		if err := s.find(ctx, query, openParticipantFields, &found); err != nil {
			return nil, err
		}
		participants = append(participants, found...)
	}
	return participants, nil
}

// OpenParticipants returns the open participant sessions of the given rooms.
func (s *Store) OpenParticipants(ctx context.Context, roomSids []string) ([]OpenParticipant, error) {
	var participants []OpenParticipant
	if len(roomSids) == 0 {
		return participants, nil
	}
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionParticipants).Find(ctx,
		bson.D{{Key: "roomSid", Value: bson.D{{Key: "$in", Value: roomSids}}}, openParticipantFilter()},
		options.Find().SetProjection(openParticipantFields))
	if err != nil {
		return nil, err
	}
	err = cursor.All(ctx, &participants)
	return participants, err
}

// OpenSessionsFilter matches the open participant sessions of the given rooms of a kind not excluded.
func OpenSessionsFilter(roomSids []string, excludedKinds []string) bson.D {
	return bson.D{
		{Key: "roomSid", Value: bson.D{{Key: "$in", Value: roomSids}}},
		openParticipantFilter(),
		{Key: "kind", Value: bson.D{{Key: "$nin", Value: excludedKinds}}},
	}
}

// RoomsWithOpenSessions returns the rooms among roomSids with an open participant session of a kind not excluded.
func (s *Store) RoomsWithOpenSessions(ctx context.Context, roomSids []string, excludedKinds []string) (map[string]bool, error) {
	rooms := map[string]bool{}
	if len(roomSids) == 0 {
		return rooms, nil
	}
	s.roundTrips++
	result := s.db.Collection(schema.CollectionParticipants).Distinct(ctx, "roomSid", OpenSessionsFilter(roomSids, excludedKinds))
	var sids []string
	if err := result.Decode(&sids); err != nil {
		return nil, err
	}
	for _, sid := range sids {
		rooms[sid] = true
	}
	return rooms, nil
}

// StaleRoomQuery is the query of StaleRooms.
func StaleRoomQuery(now time.Time, grace time.Duration, statsFrom time.Time, limit int) Query {
	cutoff := now.Add(-grace)
	timeoutEnd := bson.D{{Key: "$add", Value: bson.A{"$lastEventAt",
		bson.D{{Key: "$ifNull", Value: bson.A{"$config.emptyTimeoutMs", int64(defaultEmptyTimeoutMs)}}}}}}
	return Query{
		Collection: schema.CollectionRooms,
		Filter: bson.D{
			{Key: "status", Value: "active"},
			{Key: "lastEventAt", Value: bson.D{{Key: "$lt", Value: cutoff}}},
			{Key: "lastStatsAt", Value: bson.D{{Key: "$not", Value: bson.D{{Key: "$gte", Value: statsFrom}}}}},
			{Key: "$expr", Value: bson.D{{Key: "$lt", Value: bson.A{timeoutEnd, cutoff}}}},
		},
		Sort:  bson.D{{Key: "lastEventAt", Value: 1}},
		Limit: int64(limit),
	}
}

var openRoomFields = bson.D{
	{Key: "name", Value: 1}, {Key: "nodeId", Value: 1}, {Key: "createdAt", Value: 1}, {Key: "lastEventAt", Value: 1},
	{Key: "lastStatsAt", Value: 1},
}

// StaleRooms returns, at most limit and the stalest first, the active rooms whose last event is older than their empty
// timeout plus grace and without a stats flush since statsFrom.
func (s *Store) StaleRooms(ctx context.Context, now time.Time, grace time.Duration, statsFrom time.Time, limit int) ([]OpenRoom, error) {
	var rooms []OpenRoom
	err := s.find(ctx, StaleRoomQuery(now, grace, statsFrom, limit), openRoomFields, &rooms)
	return rooms, err
}

// IdleRoomQuery is the query of IdleRooms.
func IdleRoomQuery(before time.Time, limit int) Query {
	return Query{
		Collection: schema.CollectionRooms,
		Filter: bson.D{
			{Key: "status", Value: "active"},
			{Key: "lastEventAt", Value: bson.D{{Key: "$lt", Value: before}}},
			{Key: "lastStatsAt", Value: bson.D{{Key: "$not", Value: bson.D{{Key: "$gte", Value: before}}}}},
		},
		Sort:  bson.D{{Key: "lastEventAt", Value: -1}},
		Limit: int64(limit),
	}
}

// IdleRooms returns, at most limit and the most recently active first, the active rooms without an event or a stats
// flush since before.
func (s *Store) IdleRooms(ctx context.Context, before time.Time, limit int) ([]OpenRoom, error) {
	var rooms []OpenRoom
	err := s.find(ctx, IdleRoomQuery(before, limit), openRoomFields, &rooms)
	return rooms, err
}

// Rooms returns the rooms found among sids.
func (s *Store) Rooms(ctx context.Context, sids []string) (map[string]RoomRef, error) {
	rooms := map[string]RoomRef{}
	if len(sids) == 0 {
		return rooms, nil
	}
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionRooms).Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: sids}}}},
		options.Find().SetProjection(bson.D{{Key: "name", Value: 1}, {Key: "nodeId", Value: 1}, {Key: "status", Value: 1},
			{Key: "endedAt", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var found []RoomRef
	if err := cursor.All(ctx, &found); err != nil {
		return nil, err
	}
	for _, room := range found {
		rooms[room.SID] = room
	}
	return rooms, nil
}

// OpenEgresses returns, at most limit and the oldest first, the egresses of a room that have not ended.
func (s *Store) OpenEgresses(ctx context.Context, limit int) ([]OpenEgress, error) {
	var egresses []OpenEgress
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionEgresses).Find(ctx, bson.D{
		{Key: "status", Value: bson.D{{Key: "$in", Value: projection.OpenEgressStatuses}}},
		{Key: "roomSid", Value: bson.D{{Key: "$exists", Value: true}}},
	}, options.Find().SetSort(bson.D{{Key: "startedAt", Value: 1}}).SetLimit(int64(limit)).SetProjection(bson.D{
		{Key: "roomSid", Value: 1}, {Key: "roomName", Value: 1}, {Key: "status", Value: 1}, {Key: "startedAt", Value: 1},
		{Key: "updatedAt", Value: 1},
	}))
	if err != nil {
		return nil, err
	}
	err = cursor.All(ctx, &egresses)
	return egresses, err
}

// OpenIngressSessions returns, at most limit, the ingress sessions of a room that have not ended.
func (s *Store) OpenIngressSessions(ctx context.Context, limit int) ([]OpenIngressSession, error) {
	var sessions []OpenIngressSession
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionIngressSessions).Find(ctx, bson.D{
		{Key: "status", Value: bson.D{{Key: "$in", Value: projection.OpenIngressSessionStatuses}}},
		{Key: "roomSid", Value: bson.D{{Key: "$exists", Value: true}}},
	}, options.Find().SetLimit(int64(limit)).SetProjection(bson.D{
		{Key: "ingressId", Value: 1}, {Key: "roomSid", Value: 1}, {Key: "roomName", Value: 1}, {Key: "status", Value: 1},
		{Key: "startedAt", Value: 1},
	}))
	if err != nil {
		return nil, err
	}
	err = cursor.All(ctx, &sessions)
	return sessions, err
}

// OpenParticipantsByID returns the open participant sessions among sids.
func (s *Store) OpenParticipantsByID(ctx context.Context, sids []string) ([]OpenParticipant, error) {
	var participants []OpenParticipant
	if len(sids) == 0 {
		return participants, nil
	}
	err := s.find(ctx, Query{Collection: schema.CollectionParticipants, Filter: bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: sids}}}, openParticipantFilter(),
	}}, openParticipantFields, &participants)
	return participants, err
}

// OpenRoomsByID returns the active rooms among sids.
func (s *Store) OpenRoomsByID(ctx context.Context, sids []string) ([]OpenRoom, error) {
	var rooms []OpenRoom
	if len(sids) == 0 {
		return rooms, nil
	}
	err := s.find(ctx, Query{Collection: schema.CollectionRooms, Filter: bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: sids}}}, {Key: "status", Value: "active"},
	}}, openRoomFields, &rooms)
	return rooms, err
}

// OpenEgressesByID returns the egresses among ids that have not ended.
func (s *Store) OpenEgressesByID(ctx context.Context, ids []string) ([]OpenEgress, error) {
	var egresses []OpenEgress
	if len(ids) == 0 {
		return egresses, nil
	}
	err := s.find(ctx, Query{Collection: schema.CollectionEgresses, Filter: bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}},
		{Key: "status", Value: bson.D{{Key: "$in", Value: projection.OpenEgressStatuses}}},
	}}, bson.D{{Key: "roomSid", Value: 1}, {Key: "roomName", Value: 1}, {Key: "status", Value: 1},
		{Key: "startedAt", Value: 1}, {Key: "updatedAt", Value: 1}}, &egresses)
	return egresses, err
}

// OpenIngressSessionsByID returns the ingress sessions among ids that have not ended.
func (s *Store) OpenIngressSessionsByID(ctx context.Context, ids []string) ([]OpenIngressSession, error) {
	var sessions []OpenIngressSession
	if len(ids) == 0 {
		return sessions, nil
	}
	err := s.find(ctx, Query{Collection: schema.CollectionIngressSessions, Filter: bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}},
		{Key: "status", Value: bson.D{{Key: "$in", Value: projection.OpenIngressSessionStatuses}}},
	}}, bson.D{{Key: "ingressId", Value: 1}, {Key: "roomSid", Value: 1}, {Key: "roomName", Value: 1},
		{Key: "status", Value: 1}, {Key: "startedAt", Value: 1}}, &sessions)
	return sessions, err
}

// NodesLastSeen returns the last heartbeat of each node found among ids.
func (s *Store) NodesLastSeen(ctx context.Context, ids []string) (map[string]time.Time, error) {
	lastSeen := map[string]time.Time{}
	if len(ids) == 0 {
		return lastSeen, nil
	}
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionNodes).Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}},
		options.Find().SetProjection(bson.D{{Key: "lastSeenAt", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var nodes []struct {
		ID         string    `bson:"_id"`
		LastSeenAt time.Time `bson:"lastSeenAt"`
	}
	if err := cursor.All(ctx, &nodes); err != nil {
		return nil, err
	}
	for _, node := range nodes {
		lastSeen[node.ID] = node.LastSeenAt
	}
	return lastSeen, nil
}

// LiveRoomNodes returns the nodes of at most limit active rooms with a stats flush since statsFrom.
func (s *Store) LiveRoomNodes(ctx context.Context, statsFrom time.Time, limit int) ([]string, error) {
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionRooms).Find(ctx, bson.D{
		{Key: "status", Value: "active"},
		{Key: "lastStatsAt", Value: bson.D{{Key: "$gte", Value: statsFrom}}},
	}, options.Find().SetLimit(int64(limit)).SetProjection(bson.D{{Key: "nodeId", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var rooms []struct {
		NodeID string `bson:"nodeId"`
	}
	if err := cursor.All(ctx, &rooms); err != nil {
		return nil, err
	}
	var nodes []string
	seen := map[string]bool{}
	for _, room := range rooms {
		if room.NodeID != "" && !seen[room.NodeID] {
			seen[room.NodeID] = true
			nodes = append(nodes, room.NodeID)
		}
	}
	return nodes, nil
}

// CloseEntity applies the entity update of a closure to the document that still matches its filter, and reports
// whether it did.
func (s *Store) CloseEntity(ctx context.Context, update projection.Update) (bool, error) {
	filter := append(bson.D{{Key: "_id", Value: update.ID}}, update.Filter...)
	s.roundTrips++
	result, err := s.db.Collection(update.Collection).UpdateOne(ctx, filter, update.Pipeline)
	if err != nil {
		return false, err
	}
	return result.ModifiedCount > 0, nil
}

// ApplyCascadesCounted applies updates by filter and returns the documents modified per collection.
func (s *Store) ApplyCascadesCounted(ctx context.Context, cascades []projection.Update) (map[string]int64, error) {
	byCollection := map[string][]mongo.WriteModel{}
	var order []string
	for _, cascade := range cascades {
		if _, ok := byCollection[cascade.Collection]; !ok {
			order = append(order, cascade.Collection)
		}
		byCollection[cascade.Collection] = append(byCollection[cascade.Collection],
			mongo.NewUpdateManyModel().SetFilter(cascade.Filter).SetUpdate(cascade.Pipeline))
	}
	modified := map[string]int64{}
	var errs []error
	for _, collection := range order {
		s.roundTrips++
		result, err := s.db.Collection(collection).BulkWrite(ctx, byCollection[collection], options.BulkWrite().SetOrdered(false))
		if result != nil {
			modified[collection] = result.ModifiedCount
		}
		var bulkErr mongo.BulkWriteException
		switch {
		case err == nil:
		case errors.As(err, &bulkErr) && bulkErr.WriteConcernError == nil && len(bulkErr.WriteErrors) > 0:
			errs = append(errs, &WriteError{Collection: collection, Count: len(bulkErr.WriteErrors), Err: bulkErr.WriteErrors[0]})
		default:
			return modified, err
		}
	}
	return modified, errors.Join(errs...)
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
// [from, to), overall and per node.
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
				bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: bson.D{{Key: "node", Value: "$nodeId"},
					{Key: "identity", Value: "$identity"}}}}}},
				bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$_id.node"}, {Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}}}}},
			}},
		}}},
	}
}

// UniqueIdentities counts the distinct identities of the participant sessions that joined in [from, to), overall and
// per node.
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

// MigrationState returns the state of the legacy data migration, empty when none was recorded.
func (s *Store) MigrationState(ctx context.Context) (string, error) {
	var migration struct {
		State string `bson:"state"`
	}
	s.roundTrips++
	err := s.db.Collection(schema.CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}},
		options.FindOne().SetProjection(bson.D{{Key: "state", Value: 1}})).Decode(&migration)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	return migration.State, err
}

// RecordReconcilerRun replaces the schema_meta document of the last reconciler run.
func (s *Store) RecordReconcilerRun(ctx context.Context, run schema.SchemaMetaReconciler) error {
	run.ID = schema.SchemaMetaReconcilerID
	s.roundTrips++
	_, err := s.db.Collection(schema.CollectionSchemaMeta).ReplaceOne(ctx, bson.D{{Key: "_id", Value: run.ID}}, run,
		options.Replace().SetUpsert(true))
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
