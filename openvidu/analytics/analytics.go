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

// Package analytics writes the analytics events and stats of the server into the v2 data model in MongoDB: per-node
// batches every interval, idempotent entity updates, raw events, stats and rollups.
package analytics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bsm/redislock"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	redisLiveKit "github.com/livekit/protocol/redis"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/livekit-server/pkg/config"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
	"github.com/livekit/livekit-server/openvidu/livekithelper/livekithelperinterface"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
	"github.com/livekit/livekit-server/openvidu/openviduversion"
)

const (
	connectTimeout = 10 * time.Second
	initTimeout    = 2 * time.Minute
	// ShutdownTimeout bounds the final flush on a graceful shutdown.
	ShutdownTimeout = 5 * time.Second
)

var (
	mu         sync.Mutex
	instance   *writer
	reconciles *reconcilerRunner
	migrations *task
)

// task runs a loop in its own goroutine between Start and Stop.
type task struct {
	loop func(ctx context.Context)

	mu      sync.Mutex
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func (t *task) start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.done = make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		t.loop(ctx)
	}(t.done)
}

func (t *task) stop(ctx context.Context) {
	t.mu.Lock()
	t.stopped = true
	cancel, done := t.cancel, t.done
	t.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// reconcilerRunner runs the reconciler loop between Start and Stop.
type reconcilerRunner struct {
	task
	reconciler *reconciler
	locks      locker
}

func newReconcilerTask(r *reconciler, locks locker) *reconcilerRunner {
	runner := &reconcilerRunner{reconciler: r, locks: locks}
	runner.loop = func(ctx context.Context) { r.loop(ctx, locks) }
	return runner
}

// Node identifies the server node that writes.
type Node struct {
	ID     string
	Region string
}

// InitializeAnalytics validates the configuration, connects to MongoDB, initializes the collections, validators,
// indexes and schema_meta, and prepares the writer and the reconciler. Any failure stops the server.
func InitializeAnalytics(conf *config.Config, node Node, helper livekithelperinterface.LivekitHelper) error {
	settings, warnings, err := conf.OpenVidu.Analytics.Settings()
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		logger.Warnw(warning, nil)
	}
	logger.Infow("analytics configuration", settings.LogFields()...)

	client, err := mongo.Connect(options.Client().ApplyURI(settings.MongoURL).SetConnectTimeout(connectTimeout))
	if err != nil {
		return fmt.Errorf("analytics: connect to MongoDB: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	if err := client.Ping(ctx, nil); err != nil {
		return fmt.Errorf("analytics: MongoDB unreachable: %w", err)
	}
	db := client.Database(settings.Database)
	w, err := initialize(ctx, db, settings, node)
	if err != nil {
		return err
	}
	runner, err := newReconcilerRunner(conf, settings, db, helper, node)
	if err != nil {
		return err
	}
	migration, err := newMigrationTask(ctx, conf, settings, db, helper, node)
	if err != nil {
		return err
	}
	mu.Lock()
	instance = w
	reconciles = runner
	migrations = migration
	mu.Unlock()
	return nil
}

// newMigrationTask records a pending legacy migration when legacy collections exist and prepares its runner, or
// returns nil when nothing remains to migrate.
func newMigrationTask(ctx context.Context, conf *config.Config, settings openviduconfig.AnalyticsSettings, db *mongo.Database,
	live liveState, node Node) (*task, error) {
	pending, err := prepareMigration(ctx, db, openviduversion.Version)
	if err != nil {
		return nil, fmt.Errorf("analytics: prepare the legacy data migration: %w", err)
	}
	if !pending {
		return nil, nil
	}
	locks, err := migrationLocker(conf)
	if err != nil {
		return nil, err
	}
	rec := newReconciler(store.New(db), live, projectionOptions(settings), reconcilerSettingsOf(conf, settings), node.ID,
		newReconcilerMetrics())
	m := newMigrator(db, settings, rec, locks, node.ID, newMigrationMetrics())
	logger.Infow("analytics: legacy analytics data detected, migrating it in the background",
		"enabled", settings.Migration.Enabled, "rate", settings.Migration.Rate, "batchSize", settings.Migration.BatchSize)
	return &task{loop: m.loop}, nil
}

// migrationLocker elects the node that runs the migration without waiting for a lock another node holds.
func migrationLocker(conf *config.Config) (locker, error) {
	if !conf.Redis.IsConfigured() {
		return &localLocker{}, nil
	}
	client, err := redisLiveKit.GetRedisClient(&conf.Redis)
	if err != nil {
		return nil, fmt.Errorf("analytics: connect to Redis for the migration lock: %w", err)
	}
	return redisLocker{client: redislock.New(client)}, nil
}

// newReconcilerRunner prepares the reconciler of the node, or returns nil when it is disabled.
func newReconcilerRunner(conf *config.Config, settings openviduconfig.AnalyticsSettings, db *mongo.Database,
	live liveState, node Node) (*reconcilerRunner, error) {
	if !settings.Reconciler.Enabled {
		logger.Infow("analytics reconciler disabled: entities whose end event is lost stay open")
		return nil, nil
	}
	locks, err := reconcilerLocker(conf, settings)
	if err != nil {
		return nil, err
	}
	r := newReconciler(store.New(db), live, projectionOptions(settings), reconcilerSettingsOf(conf, settings), node.ID,
		newReconcilerMetrics())
	states := store.New(db)
	r.holdLegacy = func(ctx context.Context) (bool, error) {
		state, err := states.MigrationState(ctx)
		return state != "" && state != migrationCompleted, err
	}
	return newReconcilerTask(r, locks), nil
}

func reconcilerSettingsOf(conf *config.Config, settings openviduconfig.AnalyticsSettings) reconcilerSettings {
	return reconcilerSettings{
		batchInterval:     settings.Interval,
		interval:          settings.FixerInterval,
		cleanupInterval:   conf.OpenVidu.GetCleanupInterval(),
		maxClosures:       settings.Reconciler.MaxClosuresPerRun,
		entitiesRetention: settings.Retention.Entities,
	}
}

// reconcilerLocker elects the reconciler node with a Redis lock, or locally without Redis.
func reconcilerLocker(conf *config.Config, settings openviduconfig.AnalyticsSettings) (locker, error) {
	if !conf.Redis.IsConfigured() {
		return &localLocker{}, nil
	}
	client, err := redisLiveKit.GetRedisClient(&conf.Redis)
	if err != nil {
		return nil, fmt.Errorf("analytics: connect to Redis for the reconciler lock: %w", err)
	}
	return redisLocker{client: redislock.New(client), backoff: settings.FixerInterval / 2}, nil
}

// initialize renames the legacy events collection, initializes the database and builds the writer.
func initialize(ctx context.Context, db *mongo.Database, settings openviduconfig.AnalyticsSettings, node Node) (*writer, error) {
	if err := renameLegacyEvents(ctx, db); err != nil {
		return nil, err
	}
	result, err := schema.Initialize(ctx, db, schema.InitOptions{
		ValidationAction: settings.ValidationAction,
		Retention:        retentionOf(settings),
		StatsDetail:      settings.StatsDetail,
		WriterVersion:    openviduversion.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("analytics: initialize the database: %w", err)
	}
	logInitialization(result, settings)
	return newWriter(store.New(db), settings, node, newMetrics()), nil
}

func retentionOf(settings openviduconfig.AnalyticsSettings) schema.Retention {
	return schema.Retention{
		Events:        settings.Retention.Events,
		Stats:         settings.Retention.Stats,
		Tracks:        settings.Retention.Tracks,
		Entities:      settings.Retention.Entities,
		RollupsHourly: settings.Retention.RollupsHourly,
	}
}

func projectionOptions(settings openviduconfig.AnalyticsSettings) projection.Options {
	return projection.Options{
		Retention:   retentionOf(settings),
		StatsDetail: settings.StatsDetail,
		Encode:      schema.EncodeOptions{StoreMetadata: settings.StoreMetadata, AnonymizeIP: settings.AnonymizeIP},
		Stores:      settings.Stores,
	}
}

func logInitialization(result *schema.InitResult, settings openviduconfig.AnalyticsSettings) {
	fields := []any{
		"serverVersion", result.ServerVersion,
		"timeseries", result.Timeseries,
		"transactions", result.Transactions,
		"createdCollections", len(result.CreatedCollections),
		"createdIndexes", len(result.CreatedIndexes),
		"updatedTTLs", result.UpdatedTTLs,
		"statsDetail", settings.StatsDetail,
	}
	if result.TimeseriesError != nil {
		logger.Infow("analytics: time-series collections unavailable, track_stats is a plain collection",
			append(fields, "reason", result.TimeseriesError.Error())...)
		return
	}
	logger.Infow("analytics database initialized", fields...)
}

// renameLegacyEvents renames the `events` collection of the previous writer to `legacy_events`, so that the v2
// collection takes its name and the legacy data stays available to the migration.
func renameLegacyEvents(ctx context.Context, db *mongo.Database) error {
	names, err := db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: schema.CollectionEvents}})
	if err != nil || len(names) == 0 {
		return err
	}
	legacy, err := isLegacyEvents(ctx, db.Collection(schema.CollectionEvents))
	if err != nil || !legacy {
		return err
	}
	command := bson.D{
		{Key: "renameCollection", Value: db.Name() + "." + schema.CollectionEvents},
		{Key: "to", Value: db.Name() + "." + LegacyEventsCollection},
	}
	err = db.Client().Database("admin").RunCommand(ctx, command).Err()
	var serverErr mongo.ServerError
	switch {
	case err == nil:
	case errors.As(err, &serverErr) && serverErr.HasErrorCode(namespaceNotFound):
		// Another node starting at the same time renamed it first.
		return nil
	case errors.As(err, &serverErr) && serverErr.HasErrorCode(namespaceExists):
		// Either another node renamed it first and created the v2 collection, or both collections were there.
		if legacy, checkErr := isLegacyEvents(ctx, db.Collection(schema.CollectionEvents)); checkErr != nil || !legacy {
			return checkErr
		}
		return fmt.Errorf("analytics: both %s and a legacy %s collection exist; move one of them before starting",
			LegacyEventsCollection, schema.CollectionEvents)
	default:
		return fmt.Errorf("analytics: rename the legacy events collection: %w", err)
	}
	logger.Infow("analytics: legacy events collection renamed", "to", LegacyEventsCollection)
	return nil
}

// LegacyEventsCollection holds the events of the previous writer until the migration consumes them.
const LegacyEventsCollection = "legacy_events"

const (
	namespaceNotFound = 26
	namespaceExists   = 48
)

// isLegacyEvents reports an `events` collection written by the previous writer: its documents have no `ts` and carry
// `timestamp` or `openvidu_expire_at`, and the v2 schema version was never recorded.
func isLegacyEvents(ctx context.Context, events *mongo.Collection) (bool, error) {
	var schemaDoc bson.M
	err := events.Database().Collection(schema.CollectionSchemaMeta).
		FindOne(ctx, bson.D{{Key: "_id", Value: schema.SchemaMetaSchemaID}}).Decode(&schemaDoc)
	if err == nil {
		return false, nil
	}
	if err != mongo.ErrNoDocuments {
		return false, err
	}
	count, err := events.CountDocuments(ctx, bson.D{{Key: "ts", Value: bson.D{{Key: "$exists", Value: false}}}},
		options.Count().SetLimit(1))
	return count > 0, err
}

// Start starts the reconciler and the legacy migration, and runs the batch loop until Stop. It blocks.
func Start() {
	mu.Lock()
	w, runner, migration := instance, reconciles, migrations
	mu.Unlock()
	if runner != nil {
		runner.start()
	}
	if migration != nil {
		migration.start()
	}
	if w != nil {
		w.run()
	}
}

// Stop stops the reconciler and flushes the queues through the normal batch path, waiting at most until ctx expires,
// and counts whatever remains as dropped.
func Stop(ctx context.Context) {
	mu.Lock()
	w, runner, migration := instance, reconciles, migrations
	mu.Unlock()
	if migration != nil {
		migration.stop(ctx)
	}
	if runner != nil {
		runner.stop(ctx)
	}
	if w != nil {
		w.stop(ctx)
	}
}

// OpenViduEventsIngestClient receives the analytics events of the telemetry service in place of LiveKit's gRPC
// ingest client.
type OpenViduEventsIngestClient struct {
	// Embedded only to satisfy livekit.AnalyticsRecorderService_IngestEventsClient.
	grpc.ClientStream
}

// OpenViduStatsIngestClient receives the analytics stats of the telemetry service.
type OpenViduStatsIngestClient struct {
	grpc.ClientStream
}

func NewOpenViduEventsIngestClient() OpenViduEventsIngestClient {
	return OpenViduEventsIngestClient{}
}

func NewOpenViduStatsIngestClient() OpenViduStatsIngestClient {
	return OpenViduStatsIngestClient{}
}

// Send queues events for the next batch; it never blocks.
func (OpenViduEventsIngestClient) Send(events *livekit.AnalyticsEvents) error {
	mu.Lock()
	w := instance
	mu.Unlock()
	if w != nil {
		w.enqueueEvents(events.GetEvents())
	}
	return nil
}

// Send queues stats for the next batch; it never blocks.
func (OpenViduStatsIngestClient) Send(stats *livekit.AnalyticsStats) error {
	mu.Lock()
	w := instance
	mu.Unlock()
	if w != nil {
		w.enqueueStats(stats.GetStats())
	}
	return nil
}

func (OpenViduEventsIngestClient) CloseAndRecv() (*emptypb.Empty, error) {
	return nil, nil
}

func (OpenViduStatsIngestClient) CloseAndRecv() (*emptypb.Empty, error) {
	return nil, nil
}
