# openvidu-livekit

This fork of [livekit](https://github.com/livekit/livekit) enables the storage of livekit [events](https://github.com/livekit/protocol/blob/34c5703691c674b7166ac4668d3dab044eee51f8/livekit_analytics.proto#L128) and [stats](https://github.com/livekit/protocol/blob/34c5703691c674b7166ac4668d3dab044eee51f8/livekit_analytics.proto#L64).

By default it stores them in a **MongoDB** database.

## Configuration

Extend the native livekit configuration ([`.yaml` file](https://docs.livekit.io/realtime/self-hosting/deployment/#Configuration)) with the following properties:

```yaml
openvidu:
  analytics:
    enabled: true
    mongo_url: mongodb://localhost:27017/?replicaSet=rs0&readPreference=primaryPreferred
    interval: 10s
    expiration: 768h # 32 days
```

- `enabled`: whether to enable the storage of events/stats or not.
- `mongo_url`: URL of the MongoDB deployment where to store the events/stats. This is a [Connection String](https://www.mongodb.com/docs/manual/reference/connection-string/).
- `interval`: how often the events/stats batches must be sent. It is a [time.Duration](https://pkg.go.dev/time#Duration). Default is `10s`.
- `expiration`: the time to live of the events/stats in the storage destination. It is a [time.Duration](https://pkg.go.dev/time#Duration). Default is `768h` (32 days).

## Running locally

Just run the `docker-compose.yml` file to launch the required services (`redis`, `mongo`, and optional `mongo-express`):

```bash
docker compose up
```

Install dependencies:

```bash
go mod tidy
```

Run openvidu-livekit:

```bash
export LIVEKIT_CONFIG=$(cat ${PWD}/config.yaml)
go run ./...
```

Go to `http://localhost:8081` to explore the MongoDB database with mongo-express (`admin` username and `pass` password).

## Changing the destination of events/stats

It is fairly easy to adapt the code to store the events/stats in a different place, or even in multiple places at the same time. For example, file [`redis.go`](./openvidu/analytics/redis.go) allows storing the events/stats in the very same Redis database used by the livekit deployment.

Any new implementation must comply with interface `DatabaseClient`:

```go
type DatabaseClient interface {
	InitializeDatabase() error
	SendBatch()
	FixActiveEntities()
}
```

The `InitializeDatabase` method is used to prepare the destination database (for example creating indexes). The `SendBatch` method is used to send the events/stats in batches using the specific database client methods. The `FixActiveEntities` method is used to fix any active entities that are not actually active anymore.

Appart from complying with this interface, it is also required for any new implementation to have a reference to the `LivekitHelper` interface and the generic `AnalyticsSender` type:

```go
type AnalyticsSender struct {
	eventsQueue    queue.Queue[*livekit.AnalyticsEvent]
	statsQueue     queue.Queue[*livekit.AnalyticsStat]
	databaseClient DatabaseClient
}

type BaseDatabaseClient struct {
	owner         *AnalyticsSender
	livekitHelper livekithelperinterface.LivekitHelper
}
```

This struct has two queues for storing events and stats separately in a FIFO manner, and a reference to the database client. The analytics service has a collection of `AnalyticsSender`, over which it iterates calling each `SendBatch` method.

With this in mind, this is the implementation of the MongoDB client:

```go
type MongoDatabaseClient struct {
	BaseDatabaseClient
	client                *mongo.Client
}

func NewMongoDatabaseClient(conf *openviduconfig.AnalyticsConfig, livekithelper livekithelperinterface.LivekitHelper) (*MongoDatabaseClient, error) {
	mongoDatabaseClient := &MongoDatabaseClient{
		client: // {Golang MongoDB client},
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
	// Create indexes...
	return nil
}

func (m *MongoDatabaseClient) SendBatch() {
	// Send events using the golang MongoDB client...
	// Send stats using the golang MongoDB client...
}

func (m *MongoDatabaseClient) FixActiveEntities() {
	// Fix active entities...
}
```
