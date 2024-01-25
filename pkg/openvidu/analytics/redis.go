package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/livekit/protocol/logger"
	redisLiveKit "github.com/livekit/protocol/redis"

	"github.com/livekit/livekit-server/pkg/openvidu/openviduconfig"
	"github.com/livekit/livekit-server/pkg/openvidu/queue"
	"github.com/livekit/protocol/livekit"
	"github.com/redis/go-redis/v9"
)

type RedisDatabaseClient struct {
	client redis.UniversalClient
	owner  *AnalyticsSender
}

func NewRedisDatabaseClient(conf *openviduconfig.AnalyticsConfig, redisConfig *redisLiveKit.RedisConfig) (*RedisDatabaseClient, error) {

	var err error
	redisClient, err := redisLiveKit.GetRedisClient(redisConfig)
	if err != nil {
		return nil, err
	}

	redisDatabaseClient := &RedisDatabaseClient{
		client: redisClient,
		owner:  nil,
	}
	sender := &AnalyticsSender{
		eventsQueue:    queue.NewSliceQueue[*livekit.AnalyticsEvent](),
		statsQueue:     queue.NewSliceQueue[*livekit.AnalyticsStat](),
		databaseClient: redisDatabaseClient,
	}
	redisDatabaseClient.owner = sender

	return redisDatabaseClient, nil
}

func (m *RedisDatabaseClient) InitializeDatabase() error {
	err := m.createRedisJsonIndexDocuments()
	return err
}

func (r *RedisDatabaseClient) createRedisJsonIndexDocuments() error {

	// Create text index for event "$.type"
	_, err := r.client.Do(context.Background(), "FT.CREATE", "idx:eventType", "ON", "JSON", "PREFIX", "1", "event:", "SCHEMA", "$.type", "AS", "eventType", "TEXT").Result()
	err = handleIndexCreationError(err, "$.type")
	if err != nil {
		return err
	}

	// Create text index for event "$.room.sid"
	_, err = r.client.Do(context.Background(), "FT.CREATE", "idx:eventRoomSid", "ON", "JSON", "PREFIX", "1", "event:", "SCHEMA", "$.room.sid", "AS", "eventRoomSid", "TEXT").Result()
	err = handleIndexCreationError(err, "$.room.sid")
	if err != nil {
		return err
	}

	// Create text index for event "$.participant.sid"
	_, err = r.client.Do(context.Background(), "FT.CREATE", "idx:eventParticipantSid", "ON", "JSON", "PREFIX", "1", "event:", "SCHEMA", "$.participant.sid", "AS", "eventParticipantSid", "TEXT").Result()
	err = handleIndexCreationError(err, "$.participant.sid")
	if err != nil {
		return err
	}

	// Create numeric sortable index for event "$.timestamp.seconds"
	_, err = r.client.Do(context.Background(), "FT.CREATE", "idx:eventTimestamp", "ON", "JSON", "PREFIX", "1", "event:", "SCHEMA", "$.timestamp.seconds", "AS", "eventTimestamp", "NUMERIC", "SORTABLE").Result()
	err = handleIndexCreationError(err, "$.timestamp.seconds")
	if err != nil {
		return err
	}

	logger.Infow("created redis event indexes")

	// Create text index for stat "$.room_id"
	_, err = r.client.Do(context.Background(), "FT.CREATE", "idx:statRoomId", "ON", "JSON", "PREFIX", "1", "stat:", "SCHEMA", "$.room_id", "AS", "statRoomId", "TEXT").Result()
	err = handleIndexCreationError(err, "$.room_id")
	if err != nil {
		return err
	}

	// Create text index for stat "$.participant_id"
	_, err = r.client.Do(context.Background(), "FT.CREATE", "idx:statParticipantId", "ON", "JSON", "PREFIX", "1", "stat:", "SCHEMA", "$.participant_id", "AS", "statParticipantId", "TEXT").Result()
	err = handleIndexCreationError(err, "$.participant_id")
	if err != nil {
		return err
	}

	// Create text index for stat "$.track_id"
	_, err = r.client.Do(context.Background(), "FT.CREATE", "idx:statTrackId", "ON", "JSON", "PREFIX", "1", "stat:", "SCHEMA", "$.track_id", "AS", "statTrackId", "TEXT").Result()
	err = handleIndexCreationError(err, "$.track_id")
	if err != nil {
		return err
	}

	// Create text index for stat "$.kind"
	_, err = r.client.Do(context.Background(), "FT.CREATE", "idx:statKind", "ON", "JSON", "PREFIX", "1", "stat:", "SCHEMA", "$.kind", "AS", "statKind", "TEXT").Result()
	err = handleIndexCreationError(err, "$.kind")
	if err != nil {
		return err
	}

	// Create numeric sortable index for stat "$.time_stamp.seconds"
	_, err = r.client.Do(context.Background(), "FT.CREATE", "idx:statTimestamp", "ON", "JSON", "PREFIX", "1", "stat:", "SCHEMA", "$.time_stamp.seconds", "AS", "statTimestamp", "NUMERIC", "SORTABLE").Result()
	err = handleIndexCreationError(err, "$.time_stamp.seconds")
	if err != nil {
		return err
	}

	// Create numeric sortable index for stat "$.score"
	_, err = r.client.Do(context.Background(), "FT.CREATE", "idx:statScore", "ON", "JSON", "PREFIX", "1", "stat:", "SCHEMA", "$.score", "AS", "statScore", "NUMERIC", "SORTABLE").Result()
	err = handleIndexCreationError(err, "$.score")
	if err != nil {
		return err
	}

	logger.Infow("created redis stat indexes")

	return nil
}

func handleIndexCreationError(err error, indexSchema string) error {
	if err != nil {
		if err.Error() == "Index already exists" {
			fmt.Println("Index " + indexSchema + " already exists")
			return nil
		} else {
			return err
		}
	}
	return nil
}

func (r *RedisDatabaseClient) SendBatch() {

	events := dequeEvents(r.owner.eventsQueue)
	stats := dequeStats(r.owner.statsQueue)

	if len(events) > 0 || len(stats) > 0 {

		pipelinesByRoom := make(map[string]redis.Pipeliner)
		for _, event := range events {
			if _, ok := pipelinesByRoom[event.Room.Sid]; !ok {
				pipelinesByRoom[event.Room.Sid] = r.client.Pipeline()
			}
		}
		for _, stat := range stats {
			if _, ok := pipelinesByRoom[stat.RoomId]; !ok {
				pipelinesByRoom[stat.RoomId] = r.client.Pipeline()
			}
		}

		for _, event := range events {

			eventMap := obtainMapInterfaceFromEvent(event)
			eventKey := "event:{" + event.Room.Sid + "}" + ":" + event.Type.String() + ":" + getTimestampFromStruct(event.Timestamp)

			// Store JSON object
			pipelinesByRoom[event.Room.Sid].JSONSet(context.Background(), eventKey, "$", eventMap)
			// Set expiration time
			pipelinesByRoom[event.Room.Sid].Expire(context.Background(), eventKey, time.Duration(ANALYTICS_CONFIGURATION.Expiration))
		}

		for _, stat := range stats {

			statMap := obtainMapInterfaceFromStat(stat)
			statKey := "stat:{" + stat.RoomId + "}" + ":" + stat.ParticipantId + ":" + stat.TrackId + ":" + getTimestampFromStruct(stat.TimeStamp)

			// Store JSON object
			pipelinesByRoom[stat.RoomId].JSONSet(context.Background(), statKey, "$", statMap)
			// Set expiration time
			pipelinesByRoom[stat.RoomId].Expire(context.Background(), statKey, time.Duration(ANALYTICS_CONFIGURATION.Expiration))
		}

		for _, pipeline := range pipelinesByRoom {
			pipeline.Exec(context.Background())
		}
	}
}
