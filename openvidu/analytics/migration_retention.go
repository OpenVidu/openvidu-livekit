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

package analytics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// legacyRetentionInterval spaces the checks of the legacy data retention.
const legacyRetentionInterval = time.Hour

// legacyRetention drops legacy_events and stats once the migration has completed and its rollback window,
// legacyRetainedUntil, has passed. Every node runs it without a lock: a drop of a collection that no longer exists
// succeeds.
type legacyRetention struct {
	db       *mongo.Database
	log      jobLog
	now      func() time.Time
	interval time.Duration

	// announced is the retention date last logged.
	announced *time.Time
}

func newLegacyRetention(db *mongo.Database) *legacyRetention {
	return &legacyRetention{db: db, log: defaultJobLog(), now: func() time.Time { return time.Now().UTC() },
		interval: legacyRetentionInterval}
}

// legacyRetentionTask runs the legacy data retention loop between Start and Stop.
type legacyRetentionTask struct {
	task
	retention *legacyRetention
}

// newLegacyRetentionTask prepares the legacy data retention, or returns nil when the database has no migration
// document or neither legacy_events nor stats.
func newLegacyRetentionTask(ctx context.Context, db *mongo.Database) (*legacyRetentionTask, error) {
	found, err := db.Collection(schema.CollectionSchemaMeta).CountDocuments(ctx,
		bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}})
	if err != nil {
		return nil, fmt.Errorf("analytics: read the legacy data migration: %w", err)
	}
	if found == 0 {
		return nil, nil
	}
	collections, err := retainedLegacyCollections(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("analytics: list the legacy collections: %w", err)
	}
	if len(collections) == 0 {
		return nil, nil
	}
	r := newLegacyRetention(db)
	runner := &legacyRetentionTask{retention: r}
	runner.loop = r.loop
	return runner, nil
}

// retainedLegacyCollections returns legacy_events and stats when they exist.
func retainedLegacyCollections(ctx context.Context, db *mongo.Database) ([]string, error) {
	return db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: bson.D{{Key: "$in", Value: bson.A{
		LegacyEventsCollection, legacyStatsCollection}}}}})
}

// loop checks now and then every interval until the legacy collections are dropped or there is no migration.
func (r *legacyRetention) loop(ctx context.Context) {
	for ctx.Err() == nil {
		var done bool
		err := guarded("the legacy data retention", func() error {
			var err error
			done, err = r.check(ctx)
			return err
		})
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			r.log.warnw("analytics: cannot check the legacy data retention, retrying at the next check", err)
		case done:
			return
		}
		if !sleep(ctx, r.interval) {
			return
		}
	}
}

// check drops the legacy collections when the migration has completed, its legacyRetainedUntil has passed or is
// absent, and no legacy document expires later; a later expiry becomes legacyRetainedUntil instead. It reports done
// once the collections are dropped or when there is no migration document.
func (r *legacyRetention) check(ctx context.Context) (bool, error) {
	var migration schema.SchemaMetaMigration
	err := r.db.Collection(schema.CollectionSchemaMeta).FindOne(ctx,
		bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}}).Decode(&migration)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if migration.State != migrationCompleted {
		return false, nil
	}
	now := r.now()
	if until := migration.LegacyRetainedUntil; until != nil && until.After(now) {
		r.announce(until.UTC())
		return false, nil
	}
	latest, err := latestLegacyExpiry(ctx, r.db)
	if err != nil {
		return false, err
	}
	if latest != nil && latest.After(now) {
		// Legacy documents written after the migration completed, by a node of a previous version.
		_, err := r.db.Collection(schema.CollectionSchemaMeta).UpdateOne(ctx,
			bson.D{{Key: "_id", Value: schema.SchemaMetaMigrationID}, {Key: "state", Value: migrationCompleted}},
			bson.D{{Key: "$max", Value: bson.D{{Key: "legacyRetainedUntil", Value: *latest}}}})
		if err != nil {
			return false, err
		}
		r.announce(*latest)
		return false, nil
	}
	collections, err := retainedLegacyCollections(ctx, r.db)
	if err != nil {
		return false, err
	}
	for _, name := range collections {
		if err := r.db.Collection(name).Drop(ctx); err != nil {
			return false, err
		}
	}
	if len(collections) > 0 {
		var until any
		if migration.LegacyRetainedUntil != nil {
			until = migration.LegacyRetainedUntil.UTC()
		}
		r.log.infow("analytics: legacy data dropped after its rollback window", "collections", collections,
			"legacyRetainedUntil", until)
	}
	return true, nil
}

// announce logs the date until which the legacy data is kept, once per date.
func (r *legacyRetention) announce(until time.Time) {
	if r.announced != nil && r.announced.Equal(until) {
		return
	}
	r.announced = &until
	r.log.infow("analytics: legacy data kept for rollback until "+until.Format(time.RFC3339), "legacyRetainedUntil", until)
}
