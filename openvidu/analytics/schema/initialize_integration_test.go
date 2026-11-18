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

//go:build integration

package schema_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/testutil"
)

func testOptions() schema.InitOptions {
	return schema.InitOptions{
		ValidationAction: "error",
		Retention:        schema.DefaultRetention(),
		StatsDetail:      "participant",
		WriterVersion:    "test",
	}
}

type collectionInfo struct {
	Name    string `bson:"name"`
	Type    string `bson:"type"`
	Options bson.M `bson:"options"`
}

func listCollections(t *testing.T, db *mongo.Database) map[string]collectionInfo {
	cursor, err := db.ListCollections(context.Background(), bson.D{})
	require.NoError(t, err)
	var infos []collectionInfo
	require.NoError(t, cursor.All(context.Background(), &infos))
	byName := map[string]collectionInfo{}
	for _, info := range infos {
		byName[info.Name] = info
	}
	return byName
}

type indexInfo struct {
	Name               string `bson:"name"`
	ExpireAfterSeconds *int64 `bson:"expireAfterSeconds"`
}

func listIndexes(t *testing.T, collection *mongo.Collection) map[string]indexInfo {
	cursor, err := collection.Indexes().List(context.Background())
	require.NoError(t, err)
	var infos []indexInfo
	require.NoError(t, cursor.All(context.Background(), &infos))
	byName := map[string]indexInfo{}
	for _, info := range infos {
		byName[info.Name] = info
	}
	return byName
}

// expectedIndexNames lists the contract indexes of a collection, without those of a retention class kept forever.
func expectedIndexNames(t *testing.T, collection string, timeseries bool, retention schema.Retention) []string {
	indexes, err := schema.Indexes()
	require.NoError(t, err)
	var names []string
	for _, index := range indexes {
		if index.Collection != collection || (timeseries && index.TimeseriesFallbackOnly) {
			continue
		}
		if index.RetentionClass != "" {
			if _, limited, err := retention.Of(index.RetentionClass); err != nil || !limited {
				continue
			}
		}
		names = append(names, index.Name)
	}
	sort.Strings(names)
	return names
}

func contractIndexNames(found map[string]indexInfo, expected []string) []string {
	wanted := map[string]bool{}
	for _, name := range expected {
		wanted[name] = true
	}
	var names []string
	for name := range found {
		if wanted[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// seedTracksWithFrames initializes db, records version in schema_meta and inserts the track fixtures with what a
// version 1 writer left: a frame count in each layer of a publication, both in publishStats and in the per-layer stats
// it is derived from.
func seedTracksWithFrames(t *testing.T, db *mongo.Database, version int32) {
	ctx := context.Background()
	_, err := schema.Initialize(ctx, db, testOptions())
	require.NoError(t, err)
	setSchemaVersion(t, db, version)

	tracks := db.Collection(schema.CollectionTracks)
	fixtures, err := schema.Fixtures(schema.CollectionTracks)
	require.NoError(t, err)
	for _, fixture := range fixtures {
		_, err = tracks.InsertOne(ctx, fixture)
		require.NoError(t, err)
	}
	layered := bson.D{{Key: "publishStats.layers.0", Value: bson.D{{Key: "$exists", Value: true}}}}
	bypass := options.UpdateMany().SetBypassDocumentValidation(true)
	_, err = tracks.UpdateMany(ctx, layered,
		bson.D{{Key: "$set", Value: bson.D{{Key: "publishStats.layers.$[].frames", Value: int32(900)}}}}, bypass)
	require.NoError(t, err)
	perLayer := bson.D{{Key: "$map", Value: bson.D{
		{Key: "input", Value: "$publishStats.layers"},
		{Key: "as", Value: "layer"},
		{Key: "in", Value: bson.D{{Key: "k", Value: bson.D{{Key: "$toString", Value: "$$layer.layer"}}}, {Key: "v", Value: "$$layer"}}},
	}}}
	setInternal := bson.D{{Key: "$set", Value: bson.D{
		{Key: "internal.publishStats", Value: bson.D{{Key: "$arrayToObject", Value: perLayer}}},
	}}}
	_, err = tracks.UpdateMany(ctx, layered, mongo.Pipeline{setInternal}, bypass)
	require.NoError(t, err)
}

func setSchemaVersion(t *testing.T, db *mongo.Database, version int32) {
	_, err := db.Collection(schema.CollectionSchemaMeta).UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: schema.SchemaMetaSchemaID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "version", Value: version}}}})
	require.NoError(t, err)
}

func recordedSchema(t *testing.T, db *mongo.Database) schema.SchemaMetaSchema {
	var recorded schema.SchemaMetaSchema
	require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(context.Background(),
		bson.D{{Key: "_id", Value: schema.SchemaMetaSchemaID}}).Decode(&recorded))
	return recorded
}

// countWith returns a counter of the documents of collection that have a field.
func countWith(t *testing.T, collection *mongo.Collection) func(field string) int64 {
	return func(field string) int64 {
		n, err := collection.CountDocuments(context.Background(), bson.D{{Key: field, Value: bson.D{{Key: "$exists", Value: true}}}})
		require.NoError(t, err)
		return n
	}
}

func TestInitialize(t *testing.T) {
	client := testutil.StartMongo(t)
	ctx := context.Background()

	t.Run("pipeline updates supported, nothing written", func(t *testing.T) {
		db := client.Database("init_probe")
		require.NoError(t, schema.CheckPipelineUpdates(ctx, db))
		require.Empty(t, listCollections(t, db))
	})

	t.Run("empty database", func(t *testing.T) {
		db := client.Database("init_empty")
		result, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		require.True(t, result.Timeseries)
		require.True(t, result.Transactions)
		require.NotEmpty(t, result.ServerVersion)

		definitions, err := schema.Collections()
		require.NoError(t, err)
		collections := listCollections(t, db)
		require.Len(t, result.CreatedCollections, len(definitions))

		for _, definition := range definitions {
			info, found := collections[definition.Name]
			require.True(t, found, definition.Name)

			timeseries := definition.Timeseries != nil
			if timeseries {
				require.Equal(t, "timeseries", info.Type)
				require.EqualValues(t, 7*24*3600, info.Options["expireAfterSeconds"])
			} else {
				require.Contains(t, info.Options, "validator", definition.Name)
				require.Equal(t, "error", info.Options["validationAction"], definition.Name)
			}

			expected := expectedIndexNames(t, definition.Name, timeseries, schema.DefaultRetention())
			require.Equal(t, expected, contractIndexNames(listIndexes(t, db.Collection(definition.Name)), expected), definition.Name)
		}

		var meta schema.SchemaMetaSchema
		require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: "schema"}}).Decode(&meta))
		require.EqualValues(t, schema.SchemaVersion, meta.Version)
		require.Equal(t, "test", meta.WriterVersion)

		var retention schema.SchemaMetaRetention
		require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: "retention"}}).Decode(&retention))
		require.Equal(t, (7 * 24 * time.Hour).Milliseconds(), retention.EventsMs)
		require.Equal(t, (7 * 24 * time.Hour).Milliseconds(), retention.Rollups5MinMs)
		require.Nil(t, retention.RollupsDailyMs)
		require.NotContains(t, listIndexes(t, db.Collection(schema.CollectionAnalyticsDaily)), "bucket_ttl",
			"daily rollups are kept forever by default")

		var capabilities schema.SchemaMetaCapabilities
		require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: "capabilities"}}).Decode(&capabilities))
		require.True(t, capabilities.Timeseries)

		var config schema.SchemaMetaConfig
		require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: "config"}}).Decode(&config))
		require.Equal(t, "participant", config.StatsDetail)
	})

	t.Run("fixtures pass the validators in error mode", func(t *testing.T) {
		db := client.Database("init_fixtures")
		_, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)

		definitions, err := schema.Collections()
		require.NoError(t, err)
		for _, definition := range definitions {
			fixtures, err := schema.Fixtures(definition.Name)
			require.NoError(t, err)
			collection := db.Collection(definition.Name)
			for _, fixture := range fixtures {
				if definition.Name == schema.CollectionSchemaMeta {
					filter := bson.D{{Key: "_id", Value: fixture.Lookup("_id")}}
					_, err = collection.ReplaceOne(ctx, filter, fixture, options.Replace().SetUpsert(true))
				} else {
					_, err = collection.InsertOne(ctx, fixture)
				}
				require.NoError(t, err, "%s %s", definition.Name, fixture.Lookup("_id"))
			}
		}
	})

	t.Run("validator rejects a room whose createdAt is a string", func(t *testing.T) {
		db := client.Database("init_fixtures")
		fixtures, err := schema.Fixtures(schema.CollectionRooms)
		require.NoError(t, err)

		var room bson.D
		require.NoError(t, bson.Unmarshal(fixtures[0], &room))
		for i := range room {
			switch room[i].Key {
			case "_id":
				room[i].Value = "RM_invalid"
			case "createdAt":
				room[i].Value = "2026-10-12T09:00:00Z"
			}
		}
		_, err = db.Collection(schema.CollectionRooms).InsertOne(ctx, room)
		var writeErr mongo.WriteException
		require.ErrorAs(t, err, &writeErr)
		require.Equal(t, 121, writeErr.WriteErrors[0].Code, "document validation failure")
	})

	t.Run("Upgrade from schema version 1", func(t *testing.T) {
		db := client.Database("init_upgrade_v1")
		seedTracksWithFrames(t, db, 1)
		tracks := db.Collection(schema.CollectionTracks)
		count := countWith(t, tracks)
		layered := bson.D{{Key: "publishStats.layers.0", Value: bson.D{{Key: "$exists", Value: true}}}}
		// A layer that is not a document is left as it is.
		_, err := tracks.UpdateOne(ctx, layered,
			bson.D{{Key: "$set", Value: bson.D{{Key: "internal.publishStats.9", Value: "not a layer"}}}}, options.UpdateOne().SetBypassDocumentValidation(true))
		require.NoError(t, err)
		require.NotZero(t, count("publishStats.layers.frames"))
		require.NotZero(t, count("internal.publishStats.0.frames"))
		total, err := tracks.CountDocuments(ctx, bson.D{})
		require.NoError(t, err)

		_, err = schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)

		require.Zero(t, count("publishStats.layers.frames"))
		for _, layer := range []string{"0", "1", "2"} {
			require.Zero(t, count("internal.publishStats."+layer+".frames"), "layer %s", layer)
		}
		require.NotZero(t, count("publishStats.layers.bytes"), "the rest of each layer is kept")
		require.NotZero(t, count("internal.publishStats.0.bytes"), "the rest of each layer is kept")
		require.EqualValues(t, 1, count("internal.publishStats.9"), "a layer that is not a document")
		after, err := tracks.CountDocuments(ctx, bson.D{})
		require.NoError(t, err)
		require.Equal(t, total, after)
		var recorded schema.SchemaMetaSchema
		require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(ctx,
			bson.D{{Key: "_id", Value: schema.SchemaMetaSchemaID}}).Decode(&recorded))
		require.EqualValues(t, 2, recorded.Version)
	})

	t.Run("Upgrade from schema version 1: several nodes at once", func(t *testing.T) {
		db := client.Database("init_upgrade_v1_nodes")
		seedTracksWithFrames(t, db, 1)

		errs := make([]error, 3)
		var nodes sync.WaitGroup
		for i := range errs {
			nodes.Add(1)
			go func() {
				defer nodes.Done()
				_, errs[i] = schema.Initialize(ctx, db, testOptions())
			}()
		}
		nodes.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}

		count := countWith(t, db.Collection(schema.CollectionTracks))
		require.Zero(t, count("publishStats.layers.frames"))
		require.Zero(t, count("internal.publishStats.0.frames"))
		require.EqualValues(t, 2, recordedSchema(t, db).Version)
	})

	t.Run("Upgrade from schema version 1: a database at version 2 is left untouched", func(t *testing.T) {
		db := client.Database("init_upgrade_v2")
		seedTracksWithFrames(t, db, 2)
		count := countWith(t, db.Collection(schema.CollectionTracks))
		published, internal := count("publishStats.layers.frames"), count("internal.publishStats.0.frames")
		require.NotZero(t, published)
		require.NotZero(t, internal)

		result, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		require.Zero(t, result.UpgradedDocuments)
		require.Equal(t, published, count("publishStats.layers.frames"), "no document rewritten")
		require.Equal(t, internal, count("internal.publishStats.0.frames"), "no document rewritten")
	})

	t.Run("Upgrade from schema version 1: a repeated run rewrites nothing", func(t *testing.T) {
		db := client.Database("init_upgrade_v1_repeated")
		seedTracksWithFrames(t, db, 1)
		first, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		require.NotZero(t, first.UpgradedDocuments)

		// A run stopped after the upgrade and before recording version 2 leaves version 1 for the next one.
		setSchemaVersion(t, db, 1)
		second, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		require.Zero(t, second.UpgradedDocuments)
		require.EqualValues(t, 2, recordedSchema(t, db).Version)
	})

	t.Run("Upgrade from schema version 1: a higher recorded version is not lowered", func(t *testing.T) {
		db := client.Database("init_newer_version")
		_, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		_, err = db.Collection(schema.CollectionSchemaMeta).UpdateOne(ctx,
			bson.D{{Key: "_id", Value: schema.SchemaMetaSchemaID}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "version", Value: int32(schema.SchemaVersion + 1)}, {Key: "writerVersion", Value: "newer"}}}})
		require.NoError(t, err)

		_, err = schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		recorded := recordedSchema(t, db)
		require.EqualValues(t, schema.SchemaVersion+1, recorded.Version)
		require.Equal(t, "newer", recorded.WriterVersion)
	})

	createRetiredIndex := func(t *testing.T, rooms *mongo.Collection) {
		_, err := rooms.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "status", Value: 1}, {Key: "endedAt", Value: -1}, {Key: "_id", Value: -1}},
			Options: options.Index().SetName("status_endedAt_id"),
		})
		require.NoError(t, err)
	}

	t.Run("Retired rooms index dropped", func(t *testing.T) {
		db := client.Database("init_retired_index")
		_, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		rooms := db.Collection(schema.CollectionRooms)
		createRetiredIndex(t, rooms)
		_, err = rooms.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "name", Value: 1}},
			Options: options.Index().SetName("operator_custom"),
		})
		require.NoError(t, err)

		first, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		require.Equal(t, []string{"rooms.status_endedAt_id"}, first.DroppedIndexes)
		require.Empty(t, first.CreatedIndexes)
		found := listIndexes(t, rooms)
		require.NotContains(t, found, "status_endedAt_id")
		require.Contains(t, found, "operator_custom")
		expected := expectedIndexNames(t, schema.CollectionRooms, false, schema.DefaultRetention())
		require.Equal(t, expected, contractIndexNames(found, expected))
		require.EqualValues(t, 2, recordedSchema(t, db).Version)

		second, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		require.Empty(t, second.DroppedIndexes)
		require.Empty(t, second.CreatedIndexes)
	})

	t.Run("Nodes dropping the retired index together", func(t *testing.T) {
		db := client.Database("init_retired_index_nodes")
		_, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		rooms := db.Collection(schema.CollectionRooms)
		createRetiredIndex(t, rooms)

		errs := make([]error, 4)
		var nodes sync.WaitGroup
		for i := range errs {
			nodes.Add(1)
			go func() {
				defer nodes.Done()
				_, errs[i] = schema.Initialize(ctx, db, testOptions())
			}()
		}
		nodes.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}
		require.NotContains(t, listIndexes(t, rooms), "status_endedAt_id")
	})

	t.Run("second run changes nothing", func(t *testing.T) {
		db := client.Database("init_idempotent")
		_, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)

		result, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		require.Empty(t, result.CreatedCollections)
		require.Empty(t, result.CreatedIndexes)
		require.Empty(t, result.UpdatedTTLs)
	})

	t.Run("retention change updates TTLs and schema_meta", func(t *testing.T) {
		db := client.Database("init_retention")
		_, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)

		changed := testOptions()
		changed.Retention.Events = 720 * time.Hour
		changed.Retention.Stats = 48 * time.Hour
		result, err := schema.Initialize(ctx, db, changed)
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"events.ts_ttl", "track_stats"}, result.UpdatedTTLs,
			"5-minute rollups keep their own retention")

		events := listIndexes(t, db.Collection(schema.CollectionEvents))
		require.EqualValues(t, 2592000, *events["ts_ttl"].ExpireAfterSeconds)
		require.EqualValues(t, 48*3600, listCollections(t, db)[schema.CollectionTrackStats].Options["expireAfterSeconds"])

		var retention schema.SchemaMetaRetention
		require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: "retention"}}).Decode(&retention))
		require.Equal(t, (720 * time.Hour).Milliseconds(), retention.EventsMs)
	})

	t.Run("node documents expire with the entities retention", func(t *testing.T) {
		db := client.Database("init_nodes_retention")
		_, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		nodes := listIndexes(t, db.Collection(schema.CollectionNodes))
		require.EqualValues(t, 90*24*3600, *nodes["lastSeenAt_ttl"].ExpireAfterSeconds)
		require.Contains(t, nodes, "mediaNode_firstSeenAt")

		changed := testOptions()
		changed.Retention.Entities = 30 * 24 * time.Hour
		result, err := schema.Initialize(ctx, db, changed)
		require.NoError(t, err)
		require.Contains(t, result.UpdatedTTLs, "nodes.lastSeenAt_ttl")
		require.EqualValues(t, 30*24*3600, *listIndexes(t, db.Collection(schema.CollectionNodes))["lastSeenAt_ttl"].ExpireAfterSeconds)
	})

	t.Run("5-minute rollups retention", func(t *testing.T) {
		db := client.Database("init_rollups_5min")
		options := testOptions()
		options.Retention.Rollups5Min = 48 * time.Hour
		_, err := schema.Initialize(ctx, db, options)
		require.NoError(t, err)

		fiveMinutes := listIndexes(t, db.Collection(schema.CollectionAnalytics5Min))
		require.EqualValues(t, 48*3600, *fiveMinutes["bucket_ttl"].ExpireAfterSeconds)
		require.EqualValues(t, 7*24*3600, listCollections(t, db)[schema.CollectionTrackStats].Options["expireAfterSeconds"])

		var retention schema.SchemaMetaRetention
		require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: "retention"}}).Decode(&retention))
		require.Equal(t, (48 * time.Hour).Milliseconds(), retention.Rollups5MinMs)
	})

	t.Run("daily rollups retention set, removed and set again", func(t *testing.T) {
		db := client.Database("init_rollups_daily")
		daily := func() map[string]indexInfo { return listIndexes(t, db.Collection(schema.CollectionAnalyticsDaily)) }
		readRetention := func() schema.SchemaMetaRetention {
			var retention schema.SchemaMetaRetention
			require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: "retention"}}).Decode(&retention))
			return retention
		}
		limited := testOptions()
		limited.Retention.RollupsDaily = 1825 * 24 * time.Hour

		_, err := schema.Initialize(ctx, db, testOptions())
		require.NoError(t, err)
		require.NotContains(t, daily(), "bucket_ttl")

		result, err := schema.Initialize(ctx, db, limited)
		require.NoError(t, err)
		require.Equal(t, []string{"analytics_daily.bucket_ttl"}, result.CreatedIndexes)
		require.EqualValues(t, 157_680_000, *daily()["bucket_ttl"].ExpireAfterSeconds)
		require.EqualValues(t, 157_680_000_000, *readRetention().RollupsDailyMs)

		results := make([]*schema.InitResult, 2)
		errs := make([]error, 2)
		var nodes sync.WaitGroup
		for i := range results {
			nodes.Add(1)
			go func() {
				defer nodes.Done()
				results[i], errs[i] = schema.Initialize(ctx, db, testOptions())
			}()
		}
		nodes.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])
		require.NotContains(t, daily(), "bucket_ttl")
		require.Nil(t, readRetention().RollupsDailyMs)

		_, err = schema.Initialize(ctx, db, limited)
		require.NoError(t, err)
		require.Contains(t, daily(), "bucket_ttl")
	})

	t.Run("plain track_stats when time-series is unavailable", func(t *testing.T) {
		db := client.Database("init_fallback")
		fallback := testOptions()
		fallback.DisableTimeseries = true
		result, err := schema.Initialize(ctx, db, fallback)
		require.NoError(t, err)
		require.False(t, result.Timeseries)

		info := listCollections(t, db)[schema.CollectionTrackStats]
		require.Equal(t, "collection", info.Type)
		require.Contains(t, info.Options, "validator")

		indexes := listIndexes(t, db.Collection(schema.CollectionTrackStats))
		require.Contains(t, indexes, "ts_ttl")
		require.EqualValues(t, 7*24*3600, *indexes["ts_ttl"].ExpireAfterSeconds)
		require.Contains(t, indexes, "meta.participantSid_ts")

		var capabilities schema.SchemaMetaCapabilities
		require.NoError(t, db.Collection(schema.CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: "capabilities"}}).Decode(&capabilities))
		require.False(t, capabilities.Timeseries)

		fixtures, err := schema.Fixtures(schema.CollectionTrackStats)
		require.NoError(t, err)
		_, err = db.Collection(schema.CollectionTrackStats).InsertOne(ctx, fixtures[0])
		require.NoError(t, err)

		second, err := schema.Initialize(ctx, db, fallback)
		require.NoError(t, err)
		require.False(t, second.Timeseries)
		require.Empty(t, second.CreatedIndexes)
	})
}
