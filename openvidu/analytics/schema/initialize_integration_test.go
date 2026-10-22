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

func TestInitialize(t *testing.T) {
	client := testutil.StartMongo(t)
	ctx := context.Background()

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
