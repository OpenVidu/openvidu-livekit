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
	"strconv"
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
	client                *mongo.Client
	fakeCloseEvents       []interface{}
	deletedActiveEntities []interface{}
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

	openviduDb := m.client.Database("openvidu")
	eventCollection := openviduDb.Collection("events")
	activeEntityCollection := openviduDb.Collection("active_entities")
	ctx := context.Background()

	logger.Debugw("inserting events into MongoDB...")

	result, err := eventCollection.InsertMany(ctx, parsedEvents, options.InsertMany().SetOrdered(false))
	if err != nil {
		logger.Errorw("failed to insert events into MongoDB", err)
		logger.Warnw("restoring events for next batch", nil)
		handleInsertManyError(err, m.owner.eventsQueue, events)
		return
	} else {
		logger.Debugw("inserted events", "#", len(result.InsertedIDs))
	}

	if len(newActiveEntities) > 0 {
		logger.Debugw("inserting active entities into MongoDB...")

		result, err := activeEntityCollection.InsertMany(ctx, newActiveEntities, options.InsertMany().SetOrdered(false))
		if err != nil {
			logger.Errorw("failed to insert active entities in MongoDB", err)
		} else {
			logger.Debugw("inserted active entities", "#", len(result.InsertedIDs))
		}
	}

	if len(deletedActiveEntities) > 0 {
		logger.Debugw("deleting active entities from MongoDB...")

		result, err := activeEntityCollection.DeleteMany(ctx, bson.D{{Key: "$or", Value: deletedActiveEntities}})
		if err != nil {
			logger.Errorw("failed to delete active entities from MongoDB", err)
		} else {
			logger.Debugw("deleted active entities", "#", result.DeletedCount)
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

func (m *MongoDatabaseClient) FixActiveEntities() {
	/*
	 * IMPORTANT:
	 * Perform MongoDB operations (inserting fake close events and deleting active entities)
	 * before checking for additional entities marked as active but no longer truly active.
	 * This is crucial to avoid duplicating close events for the following reason:
	 *
	 * - There is another goroutine that runs every 10 seconds (by default) to send events to MongoDB.
	 * - This function runs every minute and may detect a closed entity as still active because the
	 *   10-second goroutine has not yet saved its authentic close event.
	 *
	 * To handle this, a fake close event for the entity is added to the list, and on the next iteration
	 * (after one minute), the process rechecks MongoDB to verify if an authentic close event has already
	 * been saved before performing further write or delete operations.
	 */
	m.filterFakeCloseEvents()

	openviduDb := m.client.Database("openvidu")
	ctx := context.Background()

	// Insert all necessary fake close events in MongoDB
	if len(m.fakeCloseEvents) > 0 {
		logger.Debugw("inserting events into MongoDB...")

		eventCollection := openviduDb.Collection("events")
		result, err := eventCollection.InsertMany(ctx, m.fakeCloseEvents, options.InsertMany().SetOrdered(false))
		if err != nil {
			logger.Errorw("failed to insert events into MongoDB", err)
			return
		} else {
			logger.Debugw("inserted events", "#", len(result.InsertedIDs))
			m.fakeCloseEvents = nil
		}
	}

	// Delete all active entities from MongoDB that are not actually active
	if len(m.deletedActiveEntities) > 0 {
		logger.Debugw("deleting active entities from MongoDB...")

		activeEntityCollection := openviduDb.Collection("active_entities")
		result, err := activeEntityCollection.DeleteMany(ctx, bson.D{{Key: "$or", Value: m.deletedActiveEntities}})
		if err != nil {
			logger.Errorw("failed to delete inactive entities from MongoDB", err)
			return
		} else {
			logger.Debugw("deleted active entities", "#", result.DeletedCount)
			m.deletedActiveEntities = nil
		}
	}

	activeEntities := m.getActiveEntities()
	lastAlive := m.getLastTimestampAlive()

	if activeEntities != nil {
		if len(activeEntities.Rooms) > 0 {
			m.fixActiveRooms(activeEntities.Rooms, lastAlive)
		}
		if len(activeEntities.Participants) > 0 {
			m.fixActiveParticipants(activeEntities.Participants, lastAlive)
		}
		if len(activeEntities.Egresses) > 0 {
			m.fixActiveEgresses(activeEntities.Egresses, lastAlive)
		}
		if len(activeEntities.Ingresses) > 0 {
			m.fixActiveIngresses(activeEntities.Ingresses, lastAlive)
		}
	}

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

	activeEntities := &ActiveEntities{}
	for _, entity := range activeEntitiesDb {
		entityTypeRaw := entity["entity"].(string)
		entityType := EntityType(entityTypeRaw)
		id := entity["_id"].(string)

		switch entityType {
		case RoomEntity:
			activeEntities.Rooms = append(activeEntities.Rooms, id)
		case ParticipantEntity:
			activeEntities.Participants = append(activeEntities.Participants, id)
		case EgressEntity:
			activeEntities.Egresses = append(activeEntities.Egresses, id)
		case IngressEntity:
			activeEntities.Ingresses = append(activeEntities.Ingresses, id)
		}
	}

	return activeEntities
}

func (m *MongoDatabaseClient) fixActiveRooms(activeRoomsDb []string, lastAlive Timestamp) {
	// Get all active rooms from LiveKit
	activeRooms, err := m.livekitHelper.ListActiveRooms()
	if err != nil {
		logger.Errorw("failed to list active rooms from LiveKit", err)
		return
	}

	activeRoomsSet := make(map[string]bool)
	for _, room := range activeRooms {
		activeRoomsSet[room.Sid] = true
	}

	// Filter rooms that are not actually active by checking if they are present in LiveKit
	for _, roomId := range activeRoomsDb {
		if !activeRoomsSet[roomId] {
			// Save "ROOM_ENDED" fake event to keep consistency
			eventCollection := m.client.Database("openvidu").Collection("events")

			// Get info from "ROOM_CREATED" event
			var roomCreatedEventMap map[string]interface{}
			err = eventCollection.FindOne(
				context.Background(),
				bson.D{
					{Key: "room.sid", Value: roomId},
					{Key: "type", Value: livekit.AnalyticsEventType_ROOM_CREATED.String()},
				},
				options.FindOne().SetProjection(bson.D{
					{Key: "_id", Value: 0},
					{Key: "room.sid", Value: 1},
					{Key: "room.name", Value: 1},
					{Key: "room.creation_time", Value: 1},
				}),
			).Decode(&roomCreatedEventMap)
			if err != nil {
				if err == mongo.ErrNoDocuments {
					m.deletedActiveEntities = append(m.deletedActiveEntities, bson.D{{Key: "_id", Value: roomId}})
				} else {
					logger.Errorw("failed to find ROOM_CREATED event in MongoDB", err, "room_id", roomId)
				}

				continue
			}

			// Fill "ROOM_ENDED" event with necessary info
			roomEndedEvent := roomCreatedEventMap
			roomEndedEvent["type"] = livekit.AnalyticsEventType_ROOM_ENDED.String()
			roomEndedEvent["room_id"] = roomId
			roomEndedEvent["openvidu_expire_at"] = time.Now().Add(ANALYTICS_CONFIGURATION.Expiration).UTC()

			creationTimeFloat := roomCreatedEventMap["room"].(map[string]interface{})["creation_time"].(float64)
			creationTime, _ := strconv.ParseInt(strconv.FormatFloat(creationTimeFloat, 'f', -1, 64), 10, 64)
			if creationTime >= lastAlive.Seconds {
				lastAlive.Seconds = creationTime + 20
			}
			roomEndedEvent["timestamp"] = lastAlive

			m.fakeCloseEvents = append(m.fakeCloseEvents, roomEndedEvent)
		}
	}
}

func (m *MongoDatabaseClient) fixActiveParticipants(activeParticipantsDb []string, lastAlive Timestamp) {
	// Get all active participants from LiveKit
	activeParticipants, err := m.livekitHelper.ListActiveParticipants()
	if err != nil {
		logger.Errorw("failed to list active participants from LiveKit", err)
		return
	}

	activeParticipantsSet := make(map[string]bool)
	for _, participant := range activeParticipants {
		activeParticipantsSet[participant.Sid] = true
	}

	// Filter participants that are not actually active by checking if they are present in LiveKit
	for _, participantId := range activeParticipantsDb {
		if !activeParticipantsSet[participantId] {
			// Save "PARTICIPANT_LEFT" fake event to keep consistency
			eventCollection := m.client.Database("openvidu").Collection("events")

			// Get info from "PARTICIPANT_ACTIVE" event
			var participantActiveEventMap map[string]interface{}
			err = eventCollection.FindOne(
				context.Background(),
				bson.D{
					{Key: "participant_id", Value: participantId},
					{Key: "type", Value: livekit.AnalyticsEventType_PARTICIPANT_ACTIVE.String()},
				},
				options.FindOne().SetProjection(bson.D{
					{Key: "_id", Value: 0},
					{Key: "room_id", Value: 1},
					{Key: "room.sid", Value: 1},
					{Key: "participant_id", Value: 1},
					{Key: "participant.sid", Value: 1},
					{Key: "participant.identity", Value: 1},
					{Key: "participant.name", Value: 1},
					{Key: "participant.joined_at", Value: 1},
				}),
			).Decode(&participantActiveEventMap)
			if err != nil {
				if err == mongo.ErrNoDocuments {
					m.deletedActiveEntities = append(m.deletedActiveEntities, bson.D{{Key: "_id", Value: participantId}})
				} else {
					logger.Errorw("failed to find PARTICIPANT_ACTIVE event in MongoDB", err, "participant_id", participantId)
				}

				continue
			}

			// Fill "PARTICIPANT_LEFT" event with necessary info
			participantLeftEvent := participantActiveEventMap
			participantLeftEvent["type"] = livekit.AnalyticsEventType_PARTICIPANT_LEFT.String()
			participantLeftEvent["openvidu_expire_at"] = time.Now().Add(ANALYTICS_CONFIGURATION.Expiration).UTC()

			joinedAtFloat := participantActiveEventMap["participant"].(map[string]interface{})["joined_at"].(float64)
			joinedAt, _ := strconv.ParseInt(strconv.FormatFloat(joinedAtFloat, 'f', -1, 64), 10, 64)
			if joinedAt >= lastAlive.Seconds {
				lastAlive.Seconds = joinedAt + 5
			}
			participantLeftEvent["timestamp"] = lastAlive

			m.fakeCloseEvents = append(m.fakeCloseEvents, participantLeftEvent)
		}
	}
}

func (m *MongoDatabaseClient) fixActiveEgresses(activeEgressesDb []string, lastAlive Timestamp) {
	// Get all active egresses from LiveKit
	activeEgresses, err := m.livekitHelper.ListActiveEgresses()
	if err != nil {
		logger.Errorw("failed to list active egresses from LiveKit", err)
		return
	}

	activeEgressesSet := make(map[string]bool)
	for _, egress := range activeEgresses {
		activeEgressesSet[egress.EgressId] = true
	}

	// Filter egresses that are not actually active by checking if they are present in LiveKit
	for _, egressId := range activeEgressesDb {
		if !activeEgressesSet[egressId] {
			// Save "EGRESS_ENDED" fake event to keep consistency
			eventCollection := m.client.Database("openvidu").Collection("events")

			// Get info from "EGRESS_STARTED" event
			var egressStartedEventMap map[string]interface{}
			err = eventCollection.FindOne(
				context.Background(),
				bson.D{
					{Key: "egress_id", Value: egressId},
					{Key: "type", Value: livekit.AnalyticsEventType_EGRESS_STARTED.String()},
				},
				options.FindOne().SetProjection(bson.D{
					{Key: "_id", Value: 0},
					{Key: "egress_id", Value: 1},
					{Key: "egress.room_id", Value: 1},
					{Key: "egress.room_name", Value: 1},
					{Key: "egress.started_at", Value: 1},
					{Key: "egress.updated_at", Value: 1},
					{Key: "egress.Request", Value: 1},
					{Key: "egress.file_results.filename", Value: 1},
					{Key: "egress.stream_results.url", Value: 1},
					{Key: "egress.segment_results.playlist_name", Value: 1},
					{Key: "timestamp.seconds", Value: 1},
				}),
			).Decode(&egressStartedEventMap)
			if err != nil {
				if err == mongo.ErrNoDocuments {
					m.deletedActiveEntities = append(m.deletedActiveEntities, bson.D{{Key: "_id", Value: egressId}})
				} else {
					logger.Errorw("failed to find EGRESS_STARTED event in MongoDB", err, "egress_id", egressId)
				}

				continue
			}

			// Fill "EGRESS_ENDED" event with necessary info
			egressEndedEvent := egressStartedEventMap
			egressEndedEvent["type"] = livekit.AnalyticsEventType_EGRESS_ENDED.String()
			egressEndedEvent["egress"].(map[string]interface{})["status"] = "EGRESS_COMPLETE"
			egressEndedEvent["openvidu_expire_at"] = time.Now().Add(ANALYTICS_CONFIGURATION.Expiration).UTC()

			float, ok := egressStartedEventMap["egress"].(map[string]interface{})["started_at"].(float64)
			if !ok {
				float, ok = egressStartedEventMap["egress"].(map[string]interface{})["updated_at"].(float64)
				if !ok {
					float = egressStartedEventMap["timestamp"].(map[string]interface{})["seconds"].(float64) * 1000000000
				}
			}
			startedAt, _ := strconv.ParseInt(strconv.FormatFloat(float, 'f', -1, 64), 10, 64)
			egressEndedEvent["egress"].(map[string]interface{})["started_at"] = startedAt
			startedAt = startedAt / 1000000000

			if startedAt >= lastAlive.Seconds {
				lastAlive.Seconds = startedAt + 5
			}
			egressEndedEvent["timestamp"] = lastAlive
			timestampInNanos := lastAlive.Seconds*1000000000 + int64(lastAlive.Nanos)
			egressEndedEvent["egress"].(map[string]interface{})["updated_at"] = timestampInNanos
			egressEndedEvent["egress"].(map[string]interface{})["ended_at"] = timestampInNanos

			m.fakeCloseEvents = append(m.fakeCloseEvents, egressEndedEvent)
		}
	}
}

func (m *MongoDatabaseClient) fixActiveIngresses(activeIngressesDb []string, lastAlive Timestamp) {
	// Get all active ingresses from LiveKit
	activeIngresses, err := m.livekitHelper.ListActiveIngresses()
	if err != nil {
		logger.Errorw("failed to list active ingresses from LiveKit", err)
		return
	}

	activeIngressesSet := make(map[string]bool)
	for _, ingress := range activeIngresses {
		activeIngressesSet[ingress.State.ResourceId] = true
	}

	// Filter ingress that are not actually active by checking if they are present in LiveKit
	for _, ingressResourceId := range activeIngressesDb {
		if !activeIngressesSet[ingressResourceId] {
			// Save "INGRESS_ENDED" fake event to keep consistency
			eventCollection := m.client.Database("openvidu").Collection("events")

			// Get info from "INGRESS_STARTED" event
			var ingressStartedEventMap map[string]interface{}
			err = eventCollection.FindOne(
				context.Background(),
				bson.D{
					{Key: "ingress.state.resource_id", Value: ingressResourceId},
					{Key: "type", Value: livekit.AnalyticsEventType_INGRESS_STARTED.String()},
				},
				options.FindOne().SetProjection(bson.D{
					{Key: "_id", Value: 0},
					{Key: "ingress_id", Value: 1},
					{Key: "ingress.state.resource_id", Value: 1},
					{Key: "ingress.state.started_at", Value: 1},
				}),
			).Decode(&ingressStartedEventMap)
			if err != nil {
				if err == mongo.ErrNoDocuments {
					m.deletedActiveEntities = append(m.deletedActiveEntities, bson.D{{Key: "_id", Value: ingressResourceId}})
				} else {
					logger.Errorw("failed to find INGRESS_STARTED event in MongoDB", err, "ingress_resource_id", ingressResourceId)
				}

				continue
			}

			// Fill "INGRESS_ENDED" event with necessary info
			ingressEndedEvent := ingressStartedEventMap
			ingressEndedEvent["type"] = livekit.AnalyticsEventType_INGRESS_ENDED.String()
			ingressEndedEvent["ingress"].(map[string]interface{})["state"].(map[string]interface{})["status"] = "ENDPOINT_INACTIVE"
			ingressEndedEvent["openvidu_expire_at"] = time.Now().Add(ANALYTICS_CONFIGURATION.Expiration).UTC()

			startedAtFloat := ingressStartedEventMap["ingress"].(map[string]interface{})["state"].(map[string]interface{})["started_at"].(float64)
			startedAt, _ := strconv.ParseInt(strconv.FormatFloat(startedAtFloat, 'f', -1, 64), 10, 64)
			startedAt = startedAt / 1000000000
			if startedAt >= lastAlive.Seconds {
				lastAlive.Seconds = startedAt + 5
			}
			ingressEndedEvent["timestamp"] = lastAlive

			m.fakeCloseEvents = append(m.fakeCloseEvents, ingressEndedEvent)
		}
	}
}

// filterFakeCloseEvents removes fake close events that are already present in MongoDB or
// adds the respective entity to the list of deleted active entities if the event is not present
func (m *MongoDatabaseClient) filterFakeCloseEvents() {
	if len(m.fakeCloseEvents) == 0 {
		return
	}

	var filteredFakeCloseEvents []interface{}
	for _, event := range m.fakeCloseEvents {
		eventType := event.(map[string]interface{})["type"].(string)
		switch eventType {
		case livekit.AnalyticsEventType_ROOM_ENDED.String():
			roomId := event.(map[string]interface{})["room_id"].(string)
			filteredFakeCloseEvents = m.filterEventsByType("room.sid", roomId, []string{eventType}, event, filteredFakeCloseEvents)
		case livekit.AnalyticsEventType_PARTICIPANT_LEFT.String():
			participantId := event.(map[string]interface{})["participant_id"].(string)
			filteredFakeCloseEvents = m.filterEventsByType("participant_id", participantId, []string{eventType}, event, filteredFakeCloseEvents)
		case livekit.AnalyticsEventType_EGRESS_ENDED.String():
			egressId := event.(map[string]interface{})["egress_id"].(string)
			filteredFakeCloseEvents = m.filterEventsByType("egress_id", egressId, []string{eventType}, event, filteredFakeCloseEvents)
		case livekit.AnalyticsEventType_INGRESS_ENDED.String():
			ingressResourceId := event.(map[string]interface{})["ingress"].(map[string]interface{})["state"].(map[string]interface{})["resource_id"].(string)
			eventTypes := []string{eventType, livekit.AnalyticsEventType_INGRESS_DELETED.String()}
			filteredFakeCloseEvents = m.filterEventsByType("ingress.state.resource_id", ingressResourceId, eventTypes, event, filteredFakeCloseEvents)
		}
	}

	m.fakeCloseEvents = filteredFakeCloseEvents
}

func (m *MongoDatabaseClient) filterEventsByType(
	idField string,
	id string,
	eventTypes []string,
	event interface{},
	filteredEvents []interface{},
) []interface{} {
	eventCollection := m.client.Database("openvidu").Collection("events")
	result := eventCollection.FindOne(
		context.Background(),
		bson.D{
			{Key: idField, Value: id},
			{Key: "type", Value: bson.D{
				{Key: "$in", Value: eventTypes},
			}},
		},
		options.FindOne().SetProjection(bson.D{{Key: idField, Value: 1}}),
	)

	if result.Err() != nil {
		if result.Err() == mongo.ErrNoDocuments {
			filteredEvents = append(filteredEvents, event)
		} else {
			logger.Errorw("failed to find close event in MongoDB", result.Err(), idField, id)
			return filteredEvents
		}
	}

	m.deletedActiveEntities = append(m.deletedActiveEntities, bson.D{{Key: "_id", Value: id}})
	return filteredEvents
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
	lastActive := LastAlive{
		ID:        "server",
		LastAlive: getCurrentTimestamp(),
	}

	_, err := lastAliveCollection.UpdateOne(
		context.Background(),
		bson.D{{Key: "_id", Value: "server"}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "_id", Value: lastActive.ID},
			{Key: "last_alive", Value: lastActive.LastAlive}},
		}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		logger.Errorw("failed to update last alive timestamp in MongoDB", err)
	}
}
