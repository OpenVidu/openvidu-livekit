# openvidu-livekit

This fork of [livekit](https://github.com/livekit/livekit) stores the analytics [events](https://github.com/livekit/protocol/blob/main/protobufs/livekit_analytics.proto) and stats of the server in a **MongoDB** database, in the data model that the OpenVidu Dashboard reads.

## Configuration

Extend the native livekit configuration ([`.yaml` file](https://docs.livekit.io/realtime/self-hosting/deployment/#Configuration)) with the `openvidu.analytics` section. The example sets every key; apart from `enabled`, `mongo_url` and `events.store_types`, the values are the defaults:

```yaml
openvidu:
  analytics:
    enabled: true
    mongo_url: mongodb://mongoadmin:mongopassword@localhost:27017/?replicaSet=rs0&readPreference=primaryPreferred
    database: openvidu
    interval: 10s
    fixer_interval: 60s
    retention:
      events: 168h
      stats: 168h
      tracks: 168h
      entities: 2160h
      rollups_hourly: 9600h
    stats_detail: participant
    anonymize_ip: false
    store_metadata: false
    events:
      store_types:
        - ROOM_CREATED
        - ROOM_ENDED
        - PARTICIPANT_JOINED
        - PARTICIPANT_ACTIVE
        - PARTICIPANT_LEFT
        - PARTICIPANT_CONNECTION_ABORTED
        - EGRESS_STARTED
        - EGRESS_ENDED
    queue:
      max_events: 20000
      max_stats: 50000
    validation_action: warn
```

| Key                        | Default                                | Meaning                                                                                                                                                                                                              |
| -------------------------- | -------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `enabled`                  | `false`                                | Write analytics.                                                                                                                                                                                                     |
| `mongo_url`                | required                               | MongoDB [connection string](https://www.mongodb.com/docs/manual/reference/connection-string/). Its password is redacted in logs.                                                                                    |
| `database`                 | `openvidu`                             | Database name.                                                                                                                                                                                                       |
| `interval`                 | `10s`                                  | Period of the write batches, from `1s` to `60s`.                                                                                                                                                                     |
| `fixer_interval`           | `60s`                                  | Reserved for the reconciliation of entities left open by stopped nodes, not active in this version. Not shorter than `interval`.                                                                                  |
| `retention.events`         | `168h`                                 | Time raw events are kept.                                                                                                                                                                                            |
| `retention.stats`          | `168h`                                 | Time statistics and 5-minute rollups are kept.                                                                                                                                                                       |
| `retention.tracks`         | `168h`                                 | Time closed tracks and subscriptions are kept.                                                                                                                                                                       |
| `retention.entities`       | `2160h`                                | Time closed rooms, participants, egress, ingress and ingress sessions are kept.                                                                                                                                      |
| `retention.rollups_hourly` | `9600h`                                | Time hourly rollups are kept. Daily rollups are kept forever.                                                                                                                                                        |
| `stats_detail`             | `participant` (CE), `track` (PRO)      | `participant`: one statistics document per participant and direction every 30 s. `track`: one per track and direction.                                                                                              |
| `anonymize_ip`             | `false`                                | Leave client IP addresses out.                                                                                                                                                                                       |
| `store_metadata`           | `false`                                | Store room and participant metadata and attributes.                                                                                                                                                                  |
| `events.store_types`       | every type but three (see below)       | Event types kept as raw events, as a YAML list or a comma-separated string. Every event updates the entities and rollups whatever this list says; `API_CALL` is never kept raw.                                   |
| `queue.max_events`         | `20000`                                | Events queued in memory, at least `1000`. The oldest one is dropped when the queue is full.                                                                                                                         |
| `queue.max_stats`          | `50000`                                | Statistics queued in memory, at least `1000`.                                                                                                                                                                        |
| `validation_action`        | `warn`                                 | `warn` logs the documents that break the collection validators; `error` rejects them (used by tests).                                                                                                               |
| `expiration`               | —                                      | **Deprecated.** Sets `retention.events` and `retention.stats` when neither is set and is ignored otherwise; a warning is logged in both cases.                                                                      |

Retentions are durations of at least `1h` in whole seconds. By default every event type the server emits is kept raw except `TRACK_PUBLISHED_UPDATE`, `TRACK_MAX_SUBSCRIBED_VIDEO_QUALITY` and `TRACK_SUBSCRIBE_REQUESTED`.

Every key can also be set with an environment variable named `LIVEKIT_OPENVIDU_ANALYTICS_` plus the key path in upper case with `_` for `.`, for example `LIVEKIT_OPENVIDU_ANALYTICS_RETENTION_EVENTS=720h` or `LIVEKIT_OPENVIDU_ANALYTICS_EVENTS_STORE_TYPES=ROOM_CREATED,ROOM_ENDED`.

An invalid value stops the server at startup with an error that names the key. The effective configuration is logged at startup.

## What is written

At startup the server creates or updates the collections, validators and indexes of the data model, whose contract is vendored in [`openvidu/analytics/schema/contract`](./openvidu/analytics/schema/contract), and records the schema version, retention and capabilities in `schema_meta`. An `events` collection written by previous versions is renamed to `legacy_events`.

Every `interval`, each node writes one batch: rooms, participants, tracks, subscriptions, egress, ingress and ingress sessions (idempotent updates, so a replayed or reordered batch converges), raw events, statistics (`track_stats`, a time-series collection when the server supports it), 5-minute, hourly and daily rollups, and its heartbeat in `nodes`. A batch that fails with a transient error (network, election) is retried first in the next interval. On a graceful shutdown the queues are written for at most 5 s.

Credentials (TURN passwords, storage keys, stream keys, tokens in URLs) are never stored: protocol messages go through an allow-list.

## Metrics

| Metric                                      | Labels                | Meaning                                         |
| ------------------------------------------- | --------------------- | ----------------------------------------------- |
| `openvidu_analytics_queue_depth`            | `queue`               | Entries waiting in the events and stats queues. |
| `openvidu_analytics_batches_total`          | `result`              | Batches by result: `success`, `retry`, `failed`. |
| `openvidu_analytics_batch_duration_seconds` |                       | Duration of a batch.                            |
| `openvidu_analytics_dropped_total`          | `queue`, `reason`     | Entries dropped: `overflow`, `error`, `unsupported`, `shutdown`. |
| `openvidu_analytics_write_errors_total`     | `collection`, `kind`  | Writes rejected by MongoDB.                     |

## Running locally

Launch the services of [`openvidu/docker-compose-setup`](./openvidu/docker-compose-setup) (Redis, MongoDB, egress, ingress, MinIO and the optional mongo-express):

```bash
docker compose -f openvidu/docker-compose-setup/docker-compose.yml up -d
```

Run openvidu-livekit:

```bash
export LIVEKIT_CONFIG=$(cat ${PWD}/config.yaml)
go run ./cmd/server
```

Go to `http://localhost:8081` to explore the MongoDB database with mongo-express (`admin` username and `pass` password).

## Tests

- Unit tests: `go test ./openvidu/...`.
- Integration tests, which need Docker: `go test -tags integration ./openvidu/analytics/...`. They start a MongoDB replica set with Testcontainers on `openvidu/mongodb:8.0.29-r1` (`ANALYTICS_TEST_MONGO_IMAGE` overrides it), or use the server of `ANALYTICS_TEST_MONGO_URI`.
- Batch benchmark: the integration test `TestBatchPerformanceBudget` runs when `ANALYTICS_BENCH_MONGO_URI` and `ANALYTICS_BENCH_DB` name a database filled by the sizing generator of the OpenVidu Dashboard (`tools/research/sizing`, scenario M, 7 days).
- The recorded events and stats used by the tests are described in [`openvidu/analytics/testdata`](./openvidu/analytics/testdata/README.md).
