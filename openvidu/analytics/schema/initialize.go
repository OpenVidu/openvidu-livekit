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

package schema

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// InitOptions configures Initialize.
type InitOptions struct {
	// ValidationAction of the collection validators: "warn" (production) or "error" (tests).
	ValidationAction string
	Retention        Retention
	// StatsDetail is "participant" or "track"; recorded in schema_meta.
	StatsDetail   string
	WriterVersion string
	// DisableTimeseries creates track_stats as a plain collection, as on servers without time-series support.
	DisableTimeseries bool
	// Now returns the instant recorded in schema_meta; time.Now when nil.
	Now func() time.Time
}

// InitResult reports what Initialize found and changed.
type InitResult struct {
	Timeseries    bool
	Transactions  bool
	ServerVersion string
	// TimeseriesError is the reason track_stats fell back to a plain collection, when it did at creation.
	TimeseriesError    error
	CreatedCollections []string
	CreatedIndexes     []string
	UpdatedTTLs        []string
	// DroppedIndexes are the TTL indexes of a retention class configured to keep its documents forever.
	DroppedIndexes []string
}

type collectionInfo struct {
	Name    string `bson:"name"`
	Type    string `bson:"type"`
	Options struct {
		ExpireAfterSeconds *int64 `bson:"expireAfterSeconds"`
	} `bson:"options"`
}

// ErrPipelineUpdatesUnsupported is the error of CheckPipelineUpdates for a database that rejects updates with an
// aggregation pipeline, such as Amazon DocumentDB: the writer projects every entity with one.
var ErrPipelineUpdatesUnsupported = errors.New("the database does not support updates with an aggregation pipeline")

// CheckPipelineUpdates asks the database to explain an update of schema_meta, without and with an aggregation
// pipeline; an explain writes nothing. It returns ErrPipelineUpdatesUnsupported when only the pipeline form is
// rejected, and any other failure as it is.
func CheckPipelineUpdates(ctx context.Context, db *mongo.Database) error {
	set := bson.D{{Key: "$set", Value: bson.D{{Key: "probe", Value: true}}}}
	return classifyPipelineProbe(explainUpdate(ctx, db, set), explainUpdate(ctx, db, bson.A{set}))
}

func explainUpdate(ctx context.Context, db *mongo.Database, update any) error {
	return db.RunCommand(ctx, bson.D{
		{Key: "explain", Value: bson.D{
			{Key: "update", Value: CollectionSchemaMeta},
			{Key: "updates", Value: bson.A{bson.D{{Key: "q", Value: bson.D{{Key: "_id", Value: "probe"}}}, {Key: "u", Value: update}}}},
		}},
		{Key: "verbosity", Value: "queryPlanner"},
	}).Err()
}

func classifyPipelineProbe(plain error, pipeline error) error {
	if pipeline == nil {
		return nil
	}
	if plain != nil {
		return fmt.Errorf("explain an update: %w", plain)
	}
	var rejected mongo.CommandError
	if errors.As(pipeline, &rejected) {
		return fmt.Errorf("%w: %s", ErrPipelineUpdatesUnsupported, rejected.Message)
	}
	return fmt.Errorf("explain an update with an aggregation pipeline: %w", pipeline)
}

// Initialize creates or updates the collections, validators, indexes and TTL values of the contract and writes the
// schema, retention, capabilities and config documents of schema_meta. It is idempotent: a second run with the same
// options changes nothing but the schema_meta timestamps.
func Initialize(ctx context.Context, db *mongo.Database, opts InitOptions) (*InitResult, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	result := &InitResult{}
	if err := detectServer(ctx, db, result); err != nil {
		return nil, err
	}

	collections, err := Collections()
	if err != nil {
		return nil, err
	}
	indexes, err := Indexes()
	if err != nil {
		return nil, err
	}

	existing, err := listCollections(ctx, db)
	if err != nil {
		return nil, err
	}

	for _, definition := range collections {
		timeseries, err := ensureCollection(ctx, db, definition, existing[definition.Name], opts, result)
		if err != nil {
			return nil, fmt.Errorf("collection %s: %w", definition.Name, err)
		}
		if definition.Timeseries != nil {
			result.Timeseries = timeseries
		}
	}

	for _, definition := range collections {
		if err := ensureIndexes(ctx, db.Collection(definition.Name), indexesOf(indexes, definition.Name), opts.Retention,
			result.Timeseries, result); err != nil {
			return nil, fmt.Errorf("indexes of %s: %w", definition.Name, err)
		}
	}

	if err := writeSchemaMeta(ctx, db, opts, result, now().UTC()); err != nil {
		return nil, fmt.Errorf("schema_meta: %w", err)
	}
	return result, nil
}

func (opts InitOptions) validate() error {
	if opts.ValidationAction != "warn" && opts.ValidationAction != "error" {
		return fmt.Errorf("validation action must be warn or error, got %q", opts.ValidationAction)
	}
	if opts.StatsDetail != "participant" && opts.StatsDetail != "track" {
		return fmt.Errorf("stats detail must be participant or track, got %q", opts.StatsDetail)
	}
	for _, class := range []RetentionClass{RetentionEvents, RetentionStats, RetentionTracks, RetentionEntities,
		RetentionRollups5Min, RetentionRollupsHourly} {
		retention, ok, err := opts.Retention.Of(class)
		if err != nil {
			return err
		}
		if !ok || !validTTL(retention) {
			return fmt.Errorf("retention %s must be a positive whole number of seconds below 2^31 s, got %s", class, retention)
		}
	}
	if daily := opts.Retention.RollupsDaily; daily < 0 || (daily > 0 && !validTTL(daily)) {
		return fmt.Errorf("retention %s must be zero (kept forever) or a whole number of seconds below 2^31 s, got %s",
			RetentionRollupsDaily, daily)
	}
	return nil
}

// validTTL reports whether a retention fits the expireAfterSeconds of a TTL index.
func validTTL(retention time.Duration) bool {
	return retention%time.Second == 0 && retention.Seconds() <= math.MaxInt32
}

func detectServer(ctx context.Context, db *mongo.Database, result *InitResult) error {
	admin := db.Client().Database("admin")

	var build struct {
		Version string `bson:"version"`
	}
	if err := admin.RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&build); err != nil {
		return fmt.Errorf("buildInfo: %w", err)
	}
	result.ServerVersion = build.Version

	var hello struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	if err := admin.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	result.Transactions = hello.SetName != "" || hello.Msg == "isdbgrid"
	return nil
}

func listCollections(ctx context.Context, db *mongo.Database) (map[string]*collectionInfo, error) {
	cursor, err := db.ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("listCollections: %w", err)
	}
	var infos []collectionInfo
	if err := cursor.All(ctx, &infos); err != nil {
		return nil, fmt.Errorf("listCollections: %w", err)
	}
	existing := make(map[string]*collectionInfo, len(infos))
	for i := range infos {
		existing[infos[i].Name] = &infos[i]
	}
	return existing, nil
}

// ensureCollection creates the collection or updates its validator (and its TTL when it is a time-series collection).
// It returns whether the collection is a time-series collection.
func ensureCollection(ctx context.Context, db *mongo.Database, definition CollectionDefinition, info *collectionInfo,
	opts InitOptions, result *InitResult) (bool, error) {
	validator, err := Validator(definition)
	if err != nil {
		return false, err
	}

	if info == nil {
		timeseries, err := createCollection(ctx, db, definition, validator, opts, result)
		if !isNamespaceExists(err) {
			return timeseries, err
		}
		// Another node starting at the same time created it first; update it as an existing collection.
		if info, err = collectionInfoOf(ctx, db, definition.Name); err != nil {
			return false, err
		}
	}

	if info.Type == "timeseries" {
		return true, ensureTimeseriesTTL(ctx, db, definition, info, opts.Retention, result)
	}

	// Time-series collections reject validators, so only plain collections (including the track_stats fallback) get one.
	return false, db.RunCommand(ctx, bson.D{
		{Key: "collMod", Value: definition.Name},
		{Key: "validator", Value: validator},
		{Key: "validationAction", Value: opts.ValidationAction},
		{Key: "validationLevel", Value: "strict"},
	}).Err()
}

func createCollection(ctx context.Context, db *mongo.Database, definition CollectionDefinition, validator bson.D,
	opts InitOptions, result *InitResult) (bool, error) {
	if definition.Timeseries != nil && !opts.DisableTimeseries {
		created, err := createTimeseries(ctx, db, definition, opts.Retention)
		if err == nil {
			result.CreatedCollections = append(result.CreatedCollections, definition.Name)
			return created, nil
		}
		if isNamespaceExists(err) {
			return false, err
		}
		result.TimeseriesError = err
	}
	createOpts := options.CreateCollection().
		SetValidator(validator).
		SetValidationAction(opts.ValidationAction).
		SetValidationLevel("strict")
	if err := db.CreateCollection(ctx, definition.Name, createOpts); err != nil {
		return false, err
	}
	result.CreatedCollections = append(result.CreatedCollections, definition.Name)
	return false, nil
}

func collectionInfoOf(ctx context.Context, db *mongo.Database, name string) (*collectionInfo, error) {
	cursor, err := db.ListCollections(ctx, bson.D{{Key: "name", Value: name}})
	if err != nil {
		return nil, fmt.Errorf("listCollections: %w", err)
	}
	var infos []collectionInfo
	if err := cursor.All(ctx, &infos); err != nil {
		return nil, fmt.Errorf("listCollections: %w", err)
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("collection %s reported as existing but not listed", name)
	}
	return &infos[0], nil
}

// isNamespaceExists reports the NamespaceExists error (code 48) of a create or rename whose target already exists.
func isNamespaceExists(err error) bool {
	var serverErr mongo.ServerError
	return errors.As(err, &serverErr) && serverErr.HasErrorCode(48)
}

func createTimeseries(ctx context.Context, db *mongo.Database, definition CollectionDefinition, retention Retention) (bool, error) {
	ttl, ok, err := retention.Of(definition.Timeseries.RetentionClass)
	if err != nil {
		return false, err
	}
	timeseries := options.TimeSeries().
		SetTimeField(definition.Timeseries.TimeField).
		SetMetaField(definition.Timeseries.MetaField).
		SetGranularity(definition.Timeseries.Granularity)
	createOpts := options.CreateCollection().SetTimeSeriesOptions(timeseries)
	if ok {
		createOpts.SetExpireAfterSeconds(int64(ttl.Seconds()))
	}
	if err := db.CreateCollection(ctx, definition.Name, createOpts); err != nil {
		return false, err
	}
	return true, nil
}

func ensureTimeseriesTTL(ctx context.Context, db *mongo.Database, definition CollectionDefinition, info *collectionInfo,
	retention Retention, result *InitResult) error {
	if definition.Timeseries == nil {
		return fmt.Errorf("time-series collection not declared as such by the contract")
	}
	ttl, ok, err := retention.Of(definition.Timeseries.RetentionClass)
	if err != nil || !ok {
		return err
	}
	seconds := int64(ttl.Seconds())
	if info.Options.ExpireAfterSeconds != nil && *info.Options.ExpireAfterSeconds == seconds {
		return nil
	}
	if err := db.RunCommand(ctx, bson.D{
		{Key: "collMod", Value: definition.Name},
		{Key: "expireAfterSeconds", Value: seconds},
	}).Err(); err != nil {
		return err
	}
	result.UpdatedTTLs = append(result.UpdatedTTLs, definition.Name)
	return nil
}

func indexesOf(indexes []IndexDefinition, collection string) []IndexDefinition {
	var selected []IndexDefinition
	for _, index := range indexes {
		if index.Collection == collection {
			selected = append(selected, index)
		}
	}
	return selected
}

type existingIndex struct {
	Name                    string   `bson:"name"`
	Key                     bson.Raw `bson:"key"`
	Unique                  bool     `bson:"unique"`
	ExpireAfterSeconds      *int64   `bson:"expireAfterSeconds"`
	PartialFilterExpression bson.Raw `bson:"partialFilterExpression"`
}

func ensureIndexes(ctx context.Context, collection *mongo.Collection, definitions []IndexDefinition, retention Retention,
	timeseries bool, result *InitResult) error {
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		return err
	}
	var current []existingIndex
	if err := cursor.All(ctx, &current); err != nil {
		return err
	}
	byName := make(map[string]existingIndex, len(current))
	for _, index := range current {
		byName[index.Name] = index
	}

	var models []mongo.IndexModel
	for _, definition := range definitions {
		if definition.TimeseriesFallbackOnly && timeseries {
			continue
		}
		ttl, keep, err := expireAfterSeconds(definition, retention)
		if err != nil {
			return err
		}

		existing, found := byName[definition.Name]
		if !keep {
			if found {
				if err := collection.Indexes().DropOne(ctx, definition.Name); err != nil && !isIndexNotFound(err) {
					return fmt.Errorf("index %s: %w", definition.Name, err)
				}
				result.DroppedIndexes = append(result.DroppedIndexes, collection.Name()+"."+definition.Name)
			}
			continue
		}
		if !found {
			models = append(models, indexModel(definition, ttl))
			continue
		}
		if err := sameIndex(definition, existing); err != nil {
			return err
		}
		if ttl != nil && (existing.ExpireAfterSeconds == nil || *existing.ExpireAfterSeconds != int64(*ttl)) {
			if err := collection.Database().RunCommand(ctx, bson.D{
				{Key: "collMod", Value: collection.Name()},
				{Key: "index", Value: bson.D{{Key: "name", Value: definition.Name}, {Key: "expireAfterSeconds", Value: *ttl}}},
			}).Err(); err != nil {
				return fmt.Errorf("index %s TTL: %w", definition.Name, err)
			}
			result.UpdatedTTLs = append(result.UpdatedTTLs, collection.Name()+"."+definition.Name)
		}
	}

	if len(models) == 0 {
		return nil
	}
	names, err := collection.Indexes().CreateMany(ctx, models)
	if err != nil {
		return err
	}
	for _, name := range names {
		result.CreatedIndexes = append(result.CreatedIndexes, collection.Name()+"."+name)
	}
	return nil
}

// expireAfterSeconds returns the TTL of an index, nil when it has none; keep is false for the index of a retention
// class that keeps its documents forever, which must not exist.
func expireAfterSeconds(definition IndexDefinition, retention Retention) (ttl *int32, keep bool, err error) {
	if definition.ExpireAfterSeconds != nil {
		return definition.ExpireAfterSeconds, true, nil
	}
	if definition.RetentionClass == "" {
		return nil, true, nil
	}
	value, ok, err := retention.Of(definition.RetentionClass)
	if err != nil {
		return nil, false, fmt.Errorf("index %s: %w", definition.Name, err)
	}
	if !ok {
		return nil, false, nil
	}
	seconds := int32(value.Seconds())
	return &seconds, true, nil
}

// isIndexNotFound reports the IndexNotFound error (code 27) of a drop that another node already made.
func isIndexNotFound(err error) bool {
	var serverErr mongo.ServerError
	return errors.As(err, &serverErr) && serverErr.HasErrorCode(27)
}

func indexModel(definition IndexDefinition, ttl *int32) mongo.IndexModel {
	indexOpts := options.Index().SetName(definition.Name)
	if definition.Unique {
		indexOpts.SetUnique(true)
	}
	if ttl != nil {
		indexOpts.SetExpireAfterSeconds(*ttl)
	}
	if definition.PartialFilterExpression != nil {
		indexOpts.SetPartialFilterExpression(definition.PartialFilterExpression)
	}
	return mongo.IndexModel{Keys: definition.Key, Options: indexOpts}
}

// sameIndex reports an error when an index with the contract's name has a different key, uniqueness or partial filter.
func sameIndex(definition IndexDefinition, existing existingIndex) error {
	elements, err := existing.Key.Elements()
	if err != nil {
		return err
	}
	mismatch := len(elements) != len(definition.Key) || existing.Unique != definition.Unique
	for i := 0; !mismatch && i < len(elements); i++ {
		direction, ok := elements[i].Value().AsInt64OK()
		mismatch = elements[i].Key() != definition.Key[i].Key || !ok || direction != asInt64(definition.Key[i].Value)
	}
	if !mismatch && definition.PartialFilterExpression == nil {
		mismatch = existing.PartialFilterExpression != nil
	} else if !mismatch {
		expected, err := bson.Marshal(definition.PartialFilterExpression)
		if err != nil {
			return err
		}
		mismatch = !bytes.Equal(expected, existing.PartialFilterExpression)
	}
	if mismatch {
		return errors.New("index " + definition.Name + " exists with a different definition; drop it to let the writer recreate it")
	}
	return nil
}

func asInt64(value any) int64 {
	switch v := value.(type) {
	case int32:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return math.MinInt64
}

func writeSchemaMeta(ctx context.Context, db *mongo.Database, opts InitOptions, result *InitResult, now time.Time) error {
	retention := SchemaMetaRetention{
		ID:              SchemaMetaRetentionID,
		EventsMs:        opts.Retention.Events.Milliseconds(),
		StatsMs:         opts.Retention.Stats.Milliseconds(),
		TracksMs:        opts.Retention.Tracks.Milliseconds(),
		EntitiesMs:      opts.Retention.Entities.Milliseconds(),
		Rollups5MinMs:   opts.Retention.Rollups5Min.Milliseconds(),
		RollupsHourlyMs: opts.Retention.RollupsHourly.Milliseconds(),
		UpdatedAt:       now,
	}
	if opts.Retention.RollupsDaily > 0 {
		daily := opts.Retention.RollupsDaily.Milliseconds()
		retention.RollupsDailyMs = &daily
	}

	documents := []any{
		SchemaMetaSchema{ID: SchemaMetaSchemaID, Version: SchemaVersion, WriterVersion: opts.WriterVersion, UpdatedAt: now},
		retention,
		SchemaMetaCapabilities{
			ID:            SchemaMetaCapabilitiesID,
			Timeseries:    result.Timeseries,
			Transactions:  result.Transactions,
			ServerVersion: result.ServerVersion,
			DetectedAt:    now,
		},
		SchemaMetaConfig{ID: SchemaMetaConfigID, StatsDetail: opts.StatsDetail, RecordedAt: now},
	}
	ids := []string{SchemaMetaSchemaID, SchemaMetaRetentionID, SchemaMetaCapabilitiesID, SchemaMetaConfigID}

	collection := db.Collection(CollectionSchemaMeta)
	for i, document := range documents {
		if _, err := collection.ReplaceOne(ctx, bson.D{{Key: "_id", Value: ids[i]}}, document,
			options.Replace().SetUpsert(true)); err != nil {
			return fmt.Errorf("%s: %w", ids[i], err)
		}
	}
	return nil
}
