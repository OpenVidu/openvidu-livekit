# openvidu-livekit

This fork of [livekit](https://github.com/livekit/livekit) enables the storage of livekit [events](https://github.com/livekit/protocol/blob/34c5703691c674b7166ac4668d3dab044eee51f8/livekit_analytics.proto#L128) and [stats](https://github.com/livekit/protocol/blob/34c5703691c674b7166ac4668d3dab044eee51f8/livekit_analytics.proto#L64).

By default it stores them in a **MongoDB** database.

## Configuration

Extend the native livekit configuration ([`.yaml` file](https://docs.livekit.io/realtime/self-hosting/deployment/#Configuration)) with the following properties:

```yaml
openvidu:
  analytics:
    enabled: true
    mongoUrl: mongodb://localhost:27017/
    interval: 10s
    expiration: 360h # 15 days
```

- `enabled`: whether to enable the storage of events/stats or not.
- `mongoUrl`: URL of the MongoDB deployment where to store the events/stats. This is a [Connection String](https://www.mongodb.com/docs/manual/reference/connection-string/).
- `interval`: how often the events/stats batches must be sent. It is a [time.Duration](https://pkg.go.dev/time#Duration)
- `expiration`: the time to live of the events/stats in the storage destination. It is a [time.Duration](https://pkg.go.dev/time#Duration)

## Running locally

Just run the `docker-compose.yml` file to launch the required services (including the MongoDB):

```bash
docker compose up
```

Run openvidu-livekit:

```bash
export LIVEKIT_CONFIG=$(cat ${PWD}/config.yaml)
go run ./...
```

## Changing the destination of events/stats

It is fairly easy to adapt the code to store the events/stats in a different place, or even in multiple places at the same time. For example, file [`redis.go`]() allows storing the events/stats in the very same Redis database used by the livekit deployment.

Any new implementation must comply with interface `DatabaseClient`:

```go
type DatabaseClient interface {
	InitializeDatabase() error
	SendBatch()
}
```

The `InitializeDatabase` method is used to prepare the destination database (for example creating indexes). The `SendBatch` method is used to send the events/stats in batches using the specific database client methods.

Appart from complying with this interface, it is also required for any new implementation to have a reference to the generic `AnalyticsSender` type:

```go
type AnalyticsSender struct {
	eventsQueue    queue.Queue[*livekit.AnalyticsEvent]
	statsQueue     queue.Queue[*livekit.AnalyticsStat]
	databaseClient DatabaseClient
}
```

This struct has two queues for storing events and stats separately in a FIFO manner, and a reference to the database client. The analytics service has a collection of `AnalyticsSender`, over which it iterates calling each `SendBatch` method.

Whith this in mind, this is the implementation of the MongoDB client:

```go
type MongoDatabaseClient struct {
	client *mongo.Client
	owner  *AnalyticsSender
}

func NewMongoDatabaseClient(conf *openviduconfig.AnalyticsConfig) (*MongoDatabaseClient, error) {
	mongoDatabaseClient := &MongoDatabaseClient{
		client: // {Golang MongoDB client},
		owner:  nil,
	}
	owner := &AnalyticsSender{
		eventsQueue:    queue.NewSliceQueue[*livekit.AnalyticsEvent](),
		statsQueue:     queue.NewSliceQueue[*livekit.AnalyticsStat](),
		databaseClient: mongoDatabaseClient,
	}
	mongoDatabaseClient.owner = owner
  return mongoDatabaseClient, nil
}

func (m *MongoDatabaseClient) InitializeDatabase() error {
  // Create indexes...
  return nil
}

func (m *MongoDatabaseClient) SendBatch() {
  // Send events using the golang MongoDB client...
  // Send stats using the golang MongoDB client...
}
```