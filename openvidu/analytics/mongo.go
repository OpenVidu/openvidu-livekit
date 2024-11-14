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

type EntityType string

const (
	RoomEntity        EntityType = "ROOM"
	ParticipantEntity EntityType = "PARTICIPANT"
	EgressEntity      EntityType = "EGRESS"
	IngressEntity     EntityType = "INGRESS"
)

type MongoDatabaseClient struct {
	client *mongo.Client
	owner  *AnalyticsSender
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
		owner:  nil,
	}

	sender := &AnalyticsSender{
		eventsQueue:    queue.NewSliceQueue[*livekit.AnalyticsEvent](),
		statsQueue:     queue.NewSliceQueue[*livekit.AnalyticsStat](),
		databaseClient: mongoDatabaseClient,
		livekitHelper:  livekithelper,
	}
	mongoDatabaseClient.owner = sender

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

	logger.Debugw("inserting events into MongoDB...")

	openviduDb := m.client.Database("openvidu")
	eventCollection := openviduDb.Collection("events")

	result, err := eventCollection.InsertMany(context.Background(), parsedEvents, options.InsertMany().SetOrdered(false))
	if err != nil {
		logger.Errorw("failed to insert events into MongoDB", err)
		logger.Warnw("restoring events for next batch", nil)
		handleInsertManyError(err, m.owner.eventsQueue, events)
	} else {
		logger.Debugw("inserted events", "#", len(result.InsertedIDs))
	}

	if len(newActiveEntities) > 0 || len(deletedActiveEntities) > 0 {
		activeEntityCollection := openviduDb.Collection("active_entities")

		if len(newActiveEntities) > 0 {
			logger.Debugw("inserting active entities into MongoDB...")

			result, err := activeEntityCollection.InsertMany(context.Background(), newActiveEntities, options.InsertMany().SetOrdered(false))
			if err != nil {
				logger.Errorw("failed to insert active entities in MongoDB", err)
			} else {
				logger.Debugw("inserted active entities", "#", len(result.InsertedIDs))
			}
		}

		if len(deletedActiveEntities) > 0 {
			logger.Debugw("deleting active entities from MongoDB...")

			result, err := activeEntityCollection.DeleteMany(context.Background(), bson.D{{Key: "$or", Value: deletedActiveEntities}})
			if err != nil {
				logger.Errorw("failed to delete active entities from MongoDB", err)
			} else {
				logger.Debugw("deleted active entities", "#", result.DeletedCount)
			}
		}
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
