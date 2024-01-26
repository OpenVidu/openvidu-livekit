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
	"errors"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/openvidu/openvidu-livekit/pkg/openvidu/openviduconfig"
	"github.com/openvidu/openvidu-livekit/pkg/openvidu/queue"
)

type MongoDatabaseClient struct {
	client *mongo.Client
	owner  *AnalyticsSender
}

func NewMongoDatabaseClient(conf *openviduconfig.AnalyticsConfig) (*MongoDatabaseClient, error) {
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
	}
	mongoDatabaseClient.owner = sender

	return mongoDatabaseClient, nil
}

func (m *MongoDatabaseClient) InitializeDatabase() error {
	err := m.createMongoJsonIndexDocuments()
	return err
}

func (m *MongoDatabaseClient) SendBatch() {

	events := dequeEvents(m.owner.eventsQueue)
	stats := dequeStats(m.owner.statsQueue)

	if len(events) > 0 || len(stats) > 0 {

		openviduDb := m.client.Database("openvidu")
		eventCollection := openviduDb.Collection("events")
		statCollection := openviduDb.Collection("stats")

		var parsedEvents []interface{}
		for _, event := range events {
			eventMap := obtainMapInterfaceFromEvent(event)
			parseEvent(eventMap, event)
			mongoParseEvent(eventMap)
			parsedEvents = append(parsedEvents, eventMap)
		}

		var parsedStats []interface{}
		for _, stat := range stats {
			statMap := obtainMapInterfaceFromStat(stat)
			parseStat(statMap, stat)
			mongoParseStat(statMap)
			parsedStats = append(parsedStats, statMap)
		}

		if len(parsedEvents) > 0 {
			logger.Debugw("inserting events into MongoDB...")

			eventsResult, err := eventCollection.InsertMany(context.Background(), parsedEvents, options.InsertMany().SetOrdered(false))

			if err != nil {
				logger.Errorw("failed to insert events into MongoDB", err)
				logger.Warnw("restoring events for next batch", nil)
				handleInsertManyError(err, m.owner.eventsQueue, events)
			} else {
				logger.Debugw("inserted events", "IDs", eventsResult.InsertedIDs)
			}
		}
		if len(parsedStats) > 0 {
			logger.Debugw("inserting stats into MongoDB...")

			statsResult, err := statCollection.InsertMany(context.Background(), parsedStats, options.InsertMany().SetOrdered(false))

			if err != nil {
				logger.Errorw("failed to insert stats into MongoDB", err)
				logger.Warnw("restoring stats for next batch", nil)
				handleInsertManyError(err, m.owner.statsQueue, stats)
			} else {
				logger.Debugw("inserted stats", "IDs", statsResult.InsertedIDs)
			}
		}
	}
}

func (m *MongoDatabaseClient) createMongoJsonIndexDocuments() error {
	context := context.TODO()
	openviduDb := m.client.Database("openvidu")
	eventCollection := openviduDb.Collection("events")
	result, err1 := eventCollection.Indexes().CreateMany(context, []mongo.IndexModel{
		{Keys: bson.D{{"type", 1}}},
		{Keys: bson.D{{"room.sid", 1}}},
		{Keys: bson.D{{"participant.sid", 1}}},
		{Keys: bson.D{{"timestamp.seconds", 1}}},
		{Keys: bson.D{{"openvidu_expire_at", 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	})
	if err1 != nil {
		logger.Errorw("failed to create MongoDB event indexes", err1)
		return err1
	}
	logger.Infow("created mongo event indexes", "result", result)

	statCollection := openviduDb.Collection("stats")
	result, err2 := statCollection.Indexes().CreateMany(context, []mongo.IndexModel{
		{Keys: bson.D{{"room_id", 1}}},
		{Keys: bson.D{{"participant_id", 1}}},
		{Keys: bson.D{{"track_id", 1}}},
		{Keys: bson.D{{"kind", 1}}},
		{Keys: bson.D{{"time_stamp.seconds", 1}}},
		{Keys: bson.D{{"score", 1}}},
		{Keys: bson.D{{"openvidu_expire_at", 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	})
	if err2 != nil {
		logger.Errorw("failed to create MongoDB stat indexes", err2)
		return err2
	}
	logger.Infow("created mongo stat indexes", "result", result)
	return nil
}

func handleInsertManyError[T *livekit.AnalyticsEvent | *livekit.AnalyticsStat](err error, queue queue.Queue[T], accumulatedCollection []T) {
	var mongoBulkWriteException mongo.BulkWriteException
	if errors.As(err, &mongoBulkWriteException) {
		// Known error BulkWriteException. Use it to restore only failed events
		for _, writeError := range mongoBulkWriteException.WriteErrors {
			queue.Enqueue(accumulatedCollection[writeError.Index])
		}
	} else {
		// Unknown error. Restore all events
		for _, event := range accumulatedCollection {
			queue.Enqueue(event)
		}
	}
}

func mongoParseEvent(eventMap map[string]interface{}) {
	eventMap["openvidu_expire_at"] = time.Now().Add(ANALYTICS_CONFIGURATION.Expiration).UTC()
}

func mongoParseStat(statMap map[string]interface{}) {
	statMap["openvidu_expire_at"] = time.Now().Add(ANALYTICS_CONFIGURATION.Expiration).UTC()
}

// func addMongoIdToEvent(eventMap map[string]interface{}, event *livekit.AnalyticsEvent) {
// 	var id string = event.Room.Sid + ":" + event.Type.String() + ":" + getTimestampFromStruct(event.Timestamp)
// 	eventMap["_id"], _ = primitive.ObjectIDFromHex(id)
// }

// func addMongoIdToStat(statMap map[string]interface{}, stat *livekit.AnalyticsStat) {
// 	statMap["_id"], _ = primitive.ObjectIDFromHex(stat.RoomId + ":" + stat.Kind.String() + ":" + stat.ParticipantId + ":" + stat.TrackId + ":" + getTimestampFromStruct(stat.TimeStamp))
// }
