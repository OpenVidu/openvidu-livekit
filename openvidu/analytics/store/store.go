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

// Package store applies the write models of the projection package to MongoDB: one unordered bulk write per
// collection, unordered inserts for raw events and stats, and the lookups a batch needs.
package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// maxStages bounds the pipeline of one update; longer merged pipelines are applied in successive rounds, which keeps
// their order since each round waits for the previous one.
const maxStages = 500

// duplicateKey is the server code of a unique index violation.
const duplicateKey = 11000

// Store writes to the analytics database. It is used by one batch at a time.
type Store struct {
	db         *mongo.Database
	roundTrips int
}

// RoundTrips returns the requests sent to the server since the last ResetRoundTrips.
func (s *Store) RoundTrips() int {
	return s.roundTrips
}

// ResetRoundTrips starts counting requests from zero.
func (s *Store) ResetRoundTrips() {
	s.roundTrips = 0
}

// New returns a store over db.
func New(db *mongo.Database) *Store {
	return &Store{db: db}
}

// WriteError is a write rejected for a reason that a retry cannot fix (validation, document too large, a value the
// driver cannot encode); the rest of the operation was applied.
type WriteError struct {
	Collection string
	Count      int
	Err        error
}

func (e *WriteError) Error() string {
	return fmt.Sprintf("%d writes to %s rejected: %v", e.Count, e.Collection, e.Err)
}

func (e *WriteError) Unwrap() error {
	return e.Err
}

// ApplyUpdates applies merged updates by document _id, in pipeline rounds of at most maxStages stages.
func (s *Store) ApplyUpdates(ctx context.Context, updates []projection.Update) error {
	var errs []error
	for round := 0; ; round++ {
		byCollection := map[string][]mongo.WriteModel{}
		var order []string
		for _, update := range updates {
			from := round * maxStages
			if from >= len(update.Pipeline) {
				continue
			}
			to := min(from+maxStages, len(update.Pipeline))
			model := mongo.NewUpdateOneModel().
				SetFilter(bson.D{{Key: "_id", Value: update.ID}}).
				SetUpdate(update.Pipeline[from:to]).
				SetUpsert(update.Upsert && round == 0)
			if _, ok := byCollection[update.Collection]; !ok {
				order = append(order, update.Collection)
			}
			byCollection[update.Collection] = append(byCollection[update.Collection], model)
		}
		if len(order) == 0 {
			return errors.Join(errs...)
		}
		for _, collection := range order {
			if err := s.bulkWrite(ctx, collection, byCollection[collection]); err != nil {
				var writeErr *WriteError
				if !errors.As(err, &writeErr) {
					return err
				}
				errs = append(errs, err)
			}
		}
	}
}

// ApplyCascades applies updates by filter to every matching document.
func (s *Store) ApplyCascades(ctx context.Context, cascades []projection.Update) error {
	byCollection := map[string][]mongo.WriteModel{}
	var order []string
	for _, cascade := range cascades {
		if _, ok := byCollection[cascade.Collection]; !ok {
			order = append(order, cascade.Collection)
		}
		byCollection[cascade.Collection] = append(byCollection[cascade.Collection],
			mongo.NewUpdateManyModel().SetFilter(cascade.Filter).SetUpdate(cascade.Pipeline))
	}
	var errs []error
	for _, collection := range order {
		if err := s.bulkWrite(ctx, collection, byCollection[collection]); err != nil {
			var writeErr *WriteError
			if !errors.As(err, &writeErr) {
				return err
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ApplyIncrements adds rollup counters, creating the rollup documents when missing.
func (s *Store) ApplyIncrements(ctx context.Context, increments []projection.Increment) error {
	byCollection := map[string][]mongo.WriteModel{}
	var order []string
	for _, increment := range increments {
		if _, ok := byCollection[increment.Collection]; !ok {
			order = append(order, increment.Collection)
		}
		byCollection[increment.Collection] = append(byCollection[increment.Collection], mongo.NewUpdateOneModel().
			SetFilter(bson.D{{Key: "_id", Value: increment.ID()}}).
			SetUpdate(projection.RenderIncrement(increment)).
			SetUpsert(true))
	}
	var errs []error
	for _, collection := range order {
		if err := s.bulkWrite(ctx, collection, byCollection[collection]); err != nil {
			var writeErr *WriteError
			if !errors.As(err, &writeErr) {
				return err
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Store) bulkWrite(ctx context.Context, collection string, models []mongo.WriteModel) error {
	if len(models) == 0 {
		return nil
	}
	s.roundTrips++
	_, err := s.db.Collection(collection).BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	if encodable, rejected := splitUnencodable(collection, err, models, encodeModel); rejected != nil {
		return withRejected(rejected, s.bulkWrite(ctx, collection, encodable))
	}
	var bulkErr mongo.BulkWriteException
	if errors.As(err, &bulkErr) && bulkErr.WriteConcernError == nil && len(bulkErr.WriteErrors) > 0 {
		return &WriteError{Collection: collection, Count: len(bulkErr.WriteErrors), Err: bulkErr.WriteErrors[0]}
	}
	return err
}

// InsertEvents inserts raw events and returns the IDs actually inserted: events already stored (duplicate key) are
// left out, which gates their rollup increments.
func (s *Store) InsertEvents(ctx context.Context, events []schema.EventDocument) (map[string]bool, error) {
	inserted := map[string]bool{}
	if len(events) == 0 {
		return inserted, nil
	}
	documents := make([]any, len(events))
	for i := range events {
		documents[i] = events[i]
		inserted[events[i].ID] = true
	}
	s.roundTrips++
	_, err := s.db.Collection(schema.CollectionEvents).InsertMany(ctx, documents, options.InsertMany().SetOrdered(false))
	if err == nil {
		return inserted, nil
	}
	if encodable, rejected := splitUnencodable(schema.CollectionEvents, err, events, encodeDocument); rejected != nil {
		inserted, err := s.InsertEvents(ctx, encodable)
		return inserted, withRejected(rejected, err)
	}
	var bulkErr mongo.BulkWriteException
	if !errors.As(err, &bulkErr) || bulkErr.WriteConcernError != nil {
		return nil, err
	}
	var rejected []mongo.BulkWriteError
	for _, writeErr := range bulkErr.WriteErrors {
		delete(inserted, events[writeErr.Index].ID)
		if writeErr.Code != duplicateKey {
			rejected = append(rejected, writeErr)
		}
	}
	if len(rejected) > 0 {
		return inserted, &WriteError{Collection: schema.CollectionEvents, Count: len(rejected), Err: rejected[0]}
	}
	return inserted, nil
}

// InsertStats inserts stats documents.
func (s *Store) InsertStats(ctx context.Context, stats []schema.TrackStatsDocument) error {
	if len(stats) == 0 {
		return nil
	}
	documents := make([]any, len(stats))
	for i := range stats {
		documents[i] = stats[i]
	}
	s.roundTrips++
	_, err := s.db.Collection(schema.CollectionTrackStats).InsertMany(ctx, documents, options.InsertMany().SetOrdered(false))
	if encodable, rejected := splitUnencodable(schema.CollectionTrackStats, err, stats, encodeDocument); rejected != nil {
		return withRejected(rejected, s.InsertStats(ctx, encodable))
	}
	var bulkErr mongo.BulkWriteException
	if errors.As(err, &bulkErr) && bulkErr.WriteConcernError == nil && len(bulkErr.WriteErrors) > 0 {
		return &WriteError{Collection: schema.CollectionTrackStats, Count: len(bulkErr.WriteErrors), Err: bulkErr.WriteErrors[0]}
	}
	return err
}

// splitUnencodable finds, after a failed write, the items the driver cannot encode, which make it fail before sending
// anything: it returns the others and a WriteError that counts them, or a nil WriteError when every item encodes. An
// error of the server, the network or a deadline never comes from encoding.
func splitUnencodable[T any](collection string, err error, items []T, encode func(T) error) ([]T, *WriteError) {
	var serverErr mongo.ServerError
	if err == nil || errors.As(err, &serverErr) || mongo.IsNetworkError(err) || mongo.IsTimeout(err) ||
		errors.Is(err, context.Canceled) {
		return nil, nil
	}
	var encodable []T
	var rejected *WriteError
	for _, item := range items {
		if encodeErr := encode(item); encodeErr != nil {
			if rejected == nil {
				rejected = &WriteError{Collection: collection, Err: encodeErr}
			}
			rejected.Count++
			continue
		}
		encodable = append(encodable, item)
	}
	return encodable, rejected
}

// withRejected adds the writes rejected before a write to its result. Any other error is returned alone: the caller
// retries or drops the whole step, which finds the unencodable items again.
func withRejected(rejected *WriteError, err error) error {
	if err == nil {
		return rejected
	}
	var writeErr *WriteError
	if !errors.As(err, &writeErr) {
		return err
	}
	return &WriteError{Collection: rejected.Collection, Count: rejected.Count + writeErr.Count, Err: rejected.Err}
}

func encodeModel(model mongo.WriteModel) error {
	var filter, update any
	switch m := model.(type) {
	case *mongo.UpdateOneModel:
		filter, update = m.Filter, m.Update
	case *mongo.UpdateManyModel:
		filter, update = m.Filter, m.Update
	}
	_, err := bson.Marshal(bson.D{{Key: "q", Value: filter}, {Key: "u", Value: update}})
	return err
}

func encodeDocument[T any](document T) error {
	_, err := bson.Marshal(document)
	return err
}

// ParticipantSessions returns, per room, the STANDARD participant sessions, for the participantsSum rollup of closed
// rooms.
func (s *Store) ParticipantSessions(ctx context.Context, roomSids []string) (map[string]int32, error) {
	sessions := map[string]int32{}
	if len(roomSids) == 0 {
		return sessions, nil
	}
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionRooms).Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: roomSids}}}},
		options.Find().SetProjection(bson.D{{Key: "participants.sessions", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var rooms []struct {
		ID           string `bson:"_id"`
		Participants struct {
			Sessions int32 `bson:"sessions"`
		} `bson:"participants"`
	}
	if err := cursor.All(ctx, &rooms); err != nil {
		return nil, err
	}
	for _, room := range rooms {
		sessions[room.ID] = room.Participants.Sessions
	}
	return sessions, nil
}

// Identities returns the identity of each participant session found.
func (s *Store) Identities(ctx context.Context, sids []string) (map[string]string, error) {
	identities := map[string]string{}
	if len(sids) == 0 {
		return identities, nil
	}
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionParticipants).Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: sids}}}},
		options.Find().SetProjection(bson.D{{Key: "identity", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var participants []struct {
		ID       string `bson:"_id"`
		Identity string `bson:"identity"`
	}
	if err := cursor.All(ctx, &participants); err != nil {
		return nil, err
	}
	for _, participant := range participants {
		identities[participant.ID] = participant.Identity
	}
	return identities, nil
}

// ExistingEvents returns which of the raw event ids are stored.
func (s *Store) ExistingEvents(ctx context.Context, ids []string) (map[string]bool, error) {
	existing := map[string]bool{}
	if len(ids) == 0 {
		return existing, nil
	}
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionEvents).Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}}, options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var events []struct {
		ID string `bson:"_id"`
	}
	if err := cursor.All(ctx, &events); err != nil {
		return nil, err
	}
	for _, event := range events {
		existing[event.ID] = true
	}
	return existing, nil
}

// Tracks returns the references of the tracks found and, per publisher among publisherSids, its published tracks.
func (s *Store) Tracks(ctx context.Context, trackSids []string, publisherSids []string) (map[string]projection.TrackRef, map[string][]string, error) {
	refs := map[string]projection.TrackRef{}
	byPublisher := map[string][]string{}
	if len(trackSids) == 0 && len(publisherSids) == 0 {
		return refs, byPublisher, nil
	}
	filter := bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: nonNil(trackSids)}}}},
		bson.D{{Key: "participantSid", Value: bson.D{{Key: "$in", Value: nonNil(publisherSids)}}}},
	}}}
	s.roundTrips++
	cursor, err := s.db.Collection(schema.CollectionTracks).Find(ctx, filter,
		options.Find().SetProjection(bson.D{{Key: "participantSid", Value: 1}, {Key: "source", Value: 1}, {Key: "type", Value: 1}}))
	if err != nil {
		return nil, nil, err
	}
	var tracks []struct {
		ID             string `bson:"_id"`
		ParticipantSID string `bson:"participantSid"`
		Source         string `bson:"source"`
		Type           string `bson:"type"`
	}
	if err := cursor.All(ctx, &tracks); err != nil {
		return nil, nil, err
	}
	for _, track := range tracks {
		refs[track.ID] = projection.TrackRef{ParticipantSID: track.ParticipantSID, Source: track.Source, Type: track.Type}
		if slices.Contains(publisherSids, track.ParticipantSID) {
			byPublisher[track.ParticipantSID] = append(byPublisher[track.ParticipantSID], track.ID)
		}
	}
	return refs, byPublisher, nil
}

// Heartbeat upserts the node document: firstSeenAt on insert, lastSeenAt advanced, version and region refreshed.
func (s *Store) Heartbeat(ctx context.Context, nodeID string, serverVersion string, region string, batchInterval time.Duration, at time.Time) error {
	set := bson.D{
		{Key: "serverVersion", Value: serverVersion},
		{Key: "batchIntervalMs", Value: int32(batchInterval.Milliseconds())},
	}
	if region != "" {
		set = append(set, bson.E{Key: "region", Value: region})
	}
	s.roundTrips++
	_, err := s.db.Collection(schema.CollectionNodes).UpdateOne(ctx, bson.D{{Key: "_id", Value: nodeID}}, bson.D{
		{Key: "$setOnInsert", Value: bson.D{{Key: "firstSeenAt", Value: at}}},
		{Key: "$max", Value: bson.D{{Key: "lastSeenAt", Value: at}}},
		{Key: "$set", Value: set},
	}, options.UpdateOne().SetUpsert(true))
	return err
}

// nonNil turns a nil slice into an empty one: $in rejects null.
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
