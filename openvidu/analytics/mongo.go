// Copyright 2024 OpenVidu
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
	"crypto/md5"
	"encoding/hex"
	"errors"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/openvidu/openvidu-livekit/openvidu/livekithelper/livekithelperinterface"
	"github.com/openvidu/openvidu-livekit/openvidu/openviduconfig"
	"github.com/openvidu/openvidu-livekit/openvidu/queue"
)

type MongoDatabaseClient struct {
	BaseDatabaseClient
	client *mongo.Client
}

func NewMongoDatabaseClient(conf *openviduconfig.AnalyticsConfig, livekithelper livekithelperinterface.LivekitHelper) (*MongoDatabaseClient, error) {
	context := context.TODO()
	mongoClient, err := mongo.Connect(context, options.Client().ApplyURI(conf.MongoUrl))
	if err != nil {
		return nil, err
	}

	logger.Infow("connecting to mongodb", "url", conf.MongoUrl)
	err = mongoClient.Ping(context, nil)
	if err != nil {
		return nil, err
	}

	mongoDatabaseClient := &MongoDatabaseClient{
		client: mongoClient,
	}

	sender := &AnalyticsSender{
		eventsQueue:    queue.NewSliceQueue[*livekit.AnalyticsEvent](),
		statsQueue:     queue.NewSliceQueue[*livekit.AnalyticsStat](),
		databaseClient: mongoDatabaseClient,
	}
	mongoDatabaseClient.owner = sender
	mongoDatabaseClient.livekitHelper = livekithelper

	return mongoDatabaseClient, nil
}

func (m *MongoDatabaseClient) InitializeDatabase() error {
	return m.createMongoJsonIndexDocuments()
}

func (m *MongoDatabaseClient) SendBatch() {
	eventsNumber := m.owner.eventsQueue.Len()
	if eventsNumber > 0 {
		m.sendEventsBatch()
	}

	statsNumber := m.owner.statsQueue.Len()
	if statsNumber > 0 {
		m.sendStatsBatch()
	}
}

func (m *MongoDatabaseClient) sendEventsBatch() {
	events := dequeEvents(m.owner.eventsQueue)

	var parsedEvents []interface{}
	var newActiveEntities []interface{}
	var deletedActiveEntities []interface{}

	for _, event := range events {
		eventMap := obtainMapInterfaceFromEvent(event)
		parseEvent(eventMap, event)
		mongoParseEvent(eventMap, event)
		parsedEvents = append(parsedEvents, eventMap)

		newActiveEntities = m.accumluateActiveEntityForCreationEvents(event, newActiveEntities)
		deletedActiveEntities = m.deleteActiveEntityForDestructionEvents(event, deletedActiveEntities)
	}

	ctx := context.Background()
	session, er := m.client.StartSession()
	if er != nil {
		logger.Errorw("failed to start session in MongoDB", er)
		return
	}
	defer session.EndSession(ctx)

	callback := func(sessCtx mongo.SessionContext) (interface{}, error) {
		openviduDb := m.client.Database("openvidu")
		eventCollection := openviduDb.Collection("events")
		activeEntityCollection := openviduDb.Collection("active_entities")

		logger.Debugw("inserting events into MongoDB...")

		result, err := eventCollection.InsertMany(sessCtx, parsedEvents, options.InsertMany().SetOrdered(false))
		if err != nil {
			logger.Errorw("failed to insert events into MongoDB", err)
			logger.Warnw("restoring events for next batch", nil)
			handleInsertManyError(err, m.owner.eventsQueue, events)
			return nil, err
		} else {
			logger.Debugw("inserted events", "#", len(result.InsertedIDs))
		}

		if len(newActiveEntities) > 0 {
			logger.Debugw("inserting active entities into MongoDB...")

			result, err := activeEntityCollection.InsertMany(sessCtx, newActiveEntities, options.InsertMany().SetOrdered(false))
			if err != nil {
				logger.Errorw("failed to insert active entities in MongoDB", err)
				return nil, err
			} else {
				logger.Debugw("inserted active entities", "#", len(result.InsertedIDs))
			}
		}

		if len(deletedActiveEntities) > 0 {
			logger.Debugw("deleting active entities from MongoDB...")

			result, err := activeEntityCollection.DeleteMany(sessCtx, bson.D{{Key: "$or", Value: deletedActiveEntities}})
			if err != nil {
				logger.Errorw("failed to delete active entities from MongoDB", err)
				return nil, err
			} else {
				logger.Debugw("deleted active entities", "#", result.DeletedCount)
			}
		}

		return nil, nil
	}

	_, err := session.WithTransaction(ctx, callback)
	if err != nil {
		logger.Errorw("failed to execute transaction in MongoDB", err)
	}
}

func (m *MongoDatabaseClient) sendStatsBatch() {
	stats := dequeStats(m.owner.statsQueue)

	var parsedStats []interface{}
	for _, stat := range stats {
		statMap := obtainMapInterfaceFromStat(stat)
		parseStat(statMap, stat)
		mongoParseStat(statMap, stat)
		parsedStats = append(parsedStats, statMap)
	}

	logger.Debugw("inserting stats into MongoDB...")

	openviduDb := m.client.Database("openvidu")
	statCollection := openviduDb.Collection("stats")

	result, err := statCollection.InsertMany(context.Background(), parsedStats, options.InsertMany().SetOrdered(false))
	if err != nil {
		logger.Errorw("failed to insert stats into MongoDB", err)
		logger.Warnw("restoring stats for next batch", nil)
		handleInsertManyError(err, m.owner.statsQueue, stats)
	} else {
		logger.Debugw("inserted stats", "#", len(result.InsertedIDs))
	}
}

func (m *MongoDatabaseClient) createMongoJsonIndexDocuments() error {
	context := context.TODO()

	openviduDb := m.client.Database("openvidu")
	logger.Infow("created database openvidu", "result", openviduDb)

	eventCollection := openviduDb.Collection("events")
	result, err := eventCollection.Indexes().CreateMany(context, []mongo.IndexModel{
		{Keys: bson.D{{Key: "type", Value: 1}}},
		{Keys: bson.D{{Key: "room.sid", Value: 1}}},
		{Keys: bson.D{{Key: "participant.sid", Value: 1}}},
		{Keys: bson.D{{Key: "timestamp.seconds", Value: 1}}},
		{Keys: bson.D{{Key: "openvidu_expire_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	})
	if err != nil {
		logger.Errorw("failed to create MongoDB event indexes", err)
		return err
	}
	logger.Infow("created mongo event indexes", "result", result)

	statCollection := openviduDb.Collection("stats")
	result, err = statCollection.Indexes().CreateMany(context, []mongo.IndexModel{
		{Keys: bson.D{{Key: "room_id", Value: 1}}},
		{Keys: bson.D{{Key: "participant_id", Value: 1}}},
		{Keys: bson.D{{Key: "track_id", Value: 1}}},
		{Keys: bson.D{{Key: "kind", Value: 1}}},
		{Keys: bson.D{{Key: "time_stamp.seconds", Value: 1}}},
		{Keys: bson.D{{Key: "score", Value: 1}}},
		{Keys: bson.D{{Key: "openvidu_expire_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	})
	if err != nil {
		logger.Errorw("failed to create MongoDB stat indexes", err)
		return err
	}
	logger.Infow("created mongo stat indexes", "result", result)

	activeEntityCollection := openviduDb.Collection("active_entities")
	resultIndex, err := activeEntityCollection.Indexes().CreateOne(context,
		mongo.IndexModel{Keys: bson.D{{Key: "entity", Value: 1}}},
	)
	if err != nil {
		logger.Errorw("failed to create MongoDB active entity index", err)
		return err
	}
	logger.Infow("created mongo active entity index", "result", resultIndex)
	return nil
}

func handleInsertManyError[T *livekit.AnalyticsEvent | *livekit.AnalyticsStat](err error, queue queue.Queue[T], accumulatedCollection []T) {
	var mongoBulkWriteException mongo.BulkWriteException
	if errors.As(err, &mongoBulkWriteException) {
		// Known error BulkWriteException. Use it to restore only failed objects
		for _, writeError := range mongoBulkWriteException.WriteErrors {
			if writeError.HasErrorCode(11000) {
				// Duplicate key error. Skip reinsertion of this event
				logger.Warnw("skipping reinsertion of duplicated object", writeError, "event", accumulatedCollection[writeError.Index])
				continue
			}
			queue.Enqueue(accumulatedCollection[writeError.Index])
		}
	} else {
		// Unknown error. Restore all objects
		for _, event := range accumulatedCollection {
			queue.Enqueue(event)
		}
	}
}

func mongoParseEvent(eventMap map[string]interface{}, event *livekit.AnalyticsEvent) {
	addMongoIdToEvent(eventMap, event)
	eventMap["openvidu_expire_at"] = time.Now().Add(ANALYTICS_CONFIGURATION.Expiration).UTC()
}

func mongoParseStat(statMap map[string]interface{}, stat *livekit.AnalyticsStat) {
	addMongoIdToStat(statMap, stat)
	statMap["openvidu_expire_at"] = time.Now().Add(ANALYTICS_CONFIGURATION.Expiration).UTC()
}

func addMongoIdToEvent(eventMap map[string]interface{}, event *livekit.AnalyticsEvent) {
	var id string
	if event.Room != nil {
		id += event.Room.Sid + ":"
	}
	if event.ParticipantId != "" {
		id += event.ParticipantId + ":"
	}
	if event.TrackId != "" {
		id += event.TrackId + ":"
	}
	id += event.Type.String() + ":" + getTimestampFromStruct(event.Timestamp)
	eventMap["_id"] = hashFromStringId(id)
}

func addMongoIdToStat(statMap map[string]interface{}, stat *livekit.AnalyticsStat) {
	var id string = stat.RoomId + ":" + stat.ParticipantId + ":" + stat.TrackId + ":" + stat.Kind.String() + ":" + stat.Node + ":" + getTimestampFromStruct(stat.TimeStamp)
	statMap["_id"] = hashFromStringId(id)
}

func hashFromStringId(id string) string {
	hash := md5.Sum([]byte(id))
	return hex.EncodeToString(hash[:])
}

func (m *MongoDatabaseClient) accumluateActiveEntityForCreationEvents(event *livekit.AnalyticsEvent, activeEntities []interface{}) []interface{} {
	var entity EntityType
	var id string

	switch event.Type {
	case livekit.AnalyticsEventType_ROOM_CREATED:
		entity = RoomEntity
		id = event.Room.Sid
	case livekit.AnalyticsEventType_PARTICIPANT_ACTIVE:
		entity = ParticipantEntity
		id = event.ParticipantId
	case livekit.AnalyticsEventType_EGRESS_STARTED:
		entity = EgressEntity
		id = event.EgressId
	case livekit.AnalyticsEventType_INGRESS_STARTED:
		entity = IngressEntity
		id = event.Ingress.State.ResourceId
	default:
		return activeEntities
	}

	return append(activeEntities, bson.D{
		{Key: "_id", Value: id},
		{Key: "entity", Value: entity},
	})
}

func (m *MongoDatabaseClient) deleteActiveEntityForDestructionEvents(event *livekit.AnalyticsEvent, activeEntities []interface{}) []interface{} {
	var id string

	switch event.Type {
	case livekit.AnalyticsEventType_ROOM_ENDED:
		id = event.RoomId
	case livekit.AnalyticsEventType_PARTICIPANT_LEFT:
		id = event.ParticipantId
	case livekit.AnalyticsEventType_EGRESS_ENDED:
		id = event.EgressId
	case livekit.AnalyticsEventType_INGRESS_ENDED:
		id = event.Ingress.State.ResourceId
	default:
		return activeEntities
	}

	return append(activeEntities, bson.D{{Key: "_id", Value: id}})
}

func (m *MongoDatabaseClient) FixActiveEntities() {
	var newEvents []interface{}
	var deletedActiveEntities []interface{}

	// Get all active entities from MongoDB
	activeEntities := m.getActiveEntities()
	// Get last timestamp that server was alive
	lastAlive := m.getLastTimestampAlive()

	// Fix active rooms
	if activeEntities != nil && len(activeEntities.Rooms) > 0 {
		deletedActiveEntities, newEvents = m.fixActiveRooms(activeEntities.Rooms, deletedActiveEntities, newEvents, lastAlive)
	}

	// TODO: Implement the following fixes
	// Fix active participants

	// Fix active egress

	// Fix active ingress

	openviduDb := m.client.Database("openvidu")
	ctx := context.Background()
	session, err := m.client.StartSession()
	if err != nil {
		logger.Errorw("failed to start session in MongoDB", err)
		return
	}
	defer session.EndSession(ctx)

	callback := func(sessCtx mongo.SessionContext) (interface{}, error) {
		// Insert all necessary close events in MongoDB
		if len(newEvents) > 0 {
			logger.Debugw("inserting events into MongoDB...")

			eventCollection := openviduDb.Collection("events")
			result, err := eventCollection.InsertMany(sessCtx, newEvents, options.InsertMany().SetOrdered(false))
			if err != nil {
				logger.Errorw("failed to insert events into MongoDB", err)
				return nil, err
			} else {
				logger.Debugw("inserted events", "#", len(result.InsertedIDs))
			}
		}

		// Delete all active entities that are not actually active from MongoDB
		if len(deletedActiveEntities) > 0 {
			logger.Debugw("deleting active entities from MongoDB...")

			activeEntityCollection := openviduDb.Collection("active_entities")
			result, err := activeEntityCollection.DeleteMany(sessCtx, bson.D{{Key: "$or", Value: deletedActiveEntities}})
			if err != nil {
				logger.Errorw("failed to delete inactive entities from MongoDB", err)
				return nil, err
			} else {
				logger.Debugw("deleted active entities", "#", result.DeletedCount)
			}
		}

		return nil, nil
	}

	_, err = session.WithTransaction(ctx, callback)
	if err != nil {
		logger.Errorw("failed to execute transaction in MongoDB", err)
	}

	// Update last timestamp that server was alive
	m.updateLastTimestampAlive()
}

func (m *MongoDatabaseClient) getActiveEntities() *ActiveEntities {
	activeEntityCollection := m.client.Database("openvidu").Collection("active_entities")

	// Get all active entities from MongoDB
	activeEntitiesCursor, err := activeEntityCollection.Find(context.Background(), bson.D{})
	if err != nil {
		logger.Errorw("failed to find active entities in MongoDB", err)
		return nil
	}

	var activeEntitiesDb []map[string]interface{}
	if err = activeEntitiesCursor.All(context.Background(), &activeEntitiesDb); err != nil {
		logger.Errorw("failed to decode active entities in MongoDB", err)
		return nil
	}

	events := m.owner.eventsQueue

	activeEntities := &ActiveEntities{}
	for _, entity := range activeEntitiesDb {
		entityType := entity["entity"].(EntityType)
		id := entity["_id"].(string)

		switch entityType {
		case RoomEntity:
			// Check if "ROOM_ENDED" event already exists
			event := &livekit.AnalyticsEvent{
				Type:   livekit.AnalyticsEventType_ROOM_ENDED,
				RoomId: id,
			}
			equalsFn := func(a, b *livekit.AnalyticsEvent) bool {
				return a.Type == b.Type && a.RoomId == b.RoomId
			}

			if !events.Contains(event, equalsFn) {
				activeEntities.Rooms = append(activeEntities.Rooms, id)
			}
		case ParticipantEntity:
			// Check if "PARTICIPANT_LEFT" event already exists
			event := &livekit.AnalyticsEvent{
				Type:          livekit.AnalyticsEventType_PARTICIPANT_LEFT,
				ParticipantId: id,
			}
			equalsFn := func(a, b *livekit.AnalyticsEvent) bool {
				return a.Type == b.Type && a.ParticipantId == b.ParticipantId
			}

			if !events.Contains(event, equalsFn) {
				activeEntities.Participants = append(activeEntities.Participants, id)
			}
		case EgressEntity:
			// Check if "EGRESS_ENDED" event already exists
			event := &livekit.AnalyticsEvent{
				Type:     livekit.AnalyticsEventType_EGRESS_ENDED,
				EgressId: id,
			}
			equalsFn := func(a, b *livekit.AnalyticsEvent) bool {
				return a.Type == b.Type && a.EgressId == b.EgressId
			}

			if !events.Contains(event, equalsFn) {
				activeEntities.Egresses = append(activeEntities.Egresses, id)
			}
		case IngressEntity:
			// Check if "INGRESS_ENDED" event already exists
			event := &livekit.AnalyticsEvent{
				Type: livekit.AnalyticsEventType_INGRESS_ENDED,
				Ingress: &livekit.IngressInfo{
					State: &livekit.IngressState{
						ResourceId: id,
					},
				},
			}
			equalsFn := func(a, b *livekit.AnalyticsEvent) bool {
				return a.Type == b.Type && a.Ingress.State.ResourceId == b.Ingress.State.ResourceId
			}

			if !events.Contains(event, equalsFn) {
				activeEntities.Ingresses = append(activeEntities.Ingresses, id)
			}
		}
	}

	return activeEntities
}

func (m *MongoDatabaseClient) fixActiveRooms(
	activeRoomsDb []string,
	deletedActiveEntities, newEvents []interface{},
	lastAlive Timestamp,
) ([]interface{}, []interface{}) {
	// Get all active rooms from LiveKit
	activeRooms, err := m.livekitHelper.ListActiveRooms()
	if err != nil {
		logger.Errorw("failed to list active rooms from LiveKit", err)
		return deletedActiveEntities, newEvents
	}

	activeRoomsSet := make(map[string]bool)
	for _, room := range activeRooms {
		activeRoomsSet[room.Sid] = true
	}

	// Filter rooms that are not actually active by checking if they are present in LiveKit
	for _, roomId := range activeRoomsDb {
		if !activeRoomsSet[roomId] {
			// Check if "ROOM_ENDED" event already exists
			eventCollection := m.client.Database("openvidu").Collection("events")
			result := eventCollection.FindOne(
				context.Background(),
				bson.D{
					{Key: "room.sid", Value: roomId},
					{Key: "type", Value: livekit.AnalyticsEventType_ROOM_ENDED.String()},
				},
				options.FindOne().SetProjection(bson.D{{Key: "room.sid", Value: 1}}),
			)

			// Check if there was an error different from "no documents found"
			if result.Err() != nil && result.Err() != mongo.ErrNoDocuments {
				logger.Errorw("failed to find ROOM_ENDED event for room in MongoDB", result.Err(), "room_id", roomId)
				continue
			}

			deletedActiveEntities = append(deletedActiveEntities, bson.D{{Key: "_id", Value: roomId}})

			// If "ROOM_ENDED" event already exists, skip
			if result.Err() == nil {
				continue
			}

			// Save "ROOM_ENDED" fake event to keep consistency
			// Get info from "ROOM_CREATED" event
			var roomCreatedEventMap map[string]interface{}
			err = eventCollection.FindOne(
				context.Background(),
				bson.D{
					{Key: "room.sid", Value: roomId},
					{Key: "type", Value: livekit.AnalyticsEventType_ROOM_CREATED.String()},
				},
				options.FindOne().SetProjection(bson.D{
					{Key: "room.sid", Value: 1},
					{Key: "room.name", Value: 1},
					{Key: "room.creation_time", Value: 1},
				}),
			).Decode(&roomCreatedEventMap)
			if err != nil {
				if err != mongo.ErrNoDocuments {
					logger.Errorw("failed to find ROOM_CREATED event for room in MongoDB", err, "room_id", roomId)
					deletedActiveEntities = deletedActiveEntities[:len(deletedActiveEntities)-1]
				}
				continue
			}

			// Fill "ROOM_ENDED" event with necessary info
			roomEndedEvent := roomCreatedEventMap
			roomEndedEvent["type"] = livekit.AnalyticsEventType_ROOM_ENDED.String()
			roomEndedEvent["room_id"] = roomId
			roomEndedEvent["openvidu_expire_at"] = time.Now().Add(ANALYTICS_CONFIGURATION.Expiration).UTC()

			creationTime := roomCreatedEventMap["room"].(map[string]interface{})["creation_time"].(int64)
			if creationTime >= lastAlive.Seconds {
				roomEndedEvent["timestamp"].(map[string]interface{})["seconds"] = creationTime + 20
			} else {
				roomEndedEvent["timestamp"].(map[string]interface{})["seconds"] = lastAlive.Seconds
			}

			newEvents = append(newEvents, roomEndedEvent)
		}
	}

	return deletedActiveEntities, newEvents
}

func (m *MongoDatabaseClient) getLastTimestampAlive() Timestamp {
	lastAliveCollection := m.client.Database("openvidu").Collection("last_alive")

	var lastAlive LastAlive
	err := lastAliveCollection.FindOne(context.Background(), bson.D{{Key: "_id", Value: "server"}}).Decode(&lastAlive)
	if err != nil {
		return getCurrentTimestamp()
	}

	return lastAlive.LastAlive
}

func (m *MongoDatabaseClient) updateLastTimestampAlive() {
	lastAliveCollection := m.client.Database("openvidu").Collection("last_alive")

	_, err := lastAliveCollection.UpdateOne(
		context.Background(),
		bson.D{{Key: "_id", Value: "server"}},
		LastAlive{
			ID:        "server",
			LastAlive: getCurrentTimestamp(),
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		logger.Errorw("failed to update last alive timestamp in MongoDB", err)
	}
}
