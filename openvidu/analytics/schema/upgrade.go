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
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// upgradeDocuments removes from the existing documents the fields that the contract version recorded in schema_meta
// had and this one has not, so that the strict validators accept their later updates. It runs after the validators
// are updated and before this version is recorded; nodes starting together may all run it.
func upgradeDocuments(ctx context.Context, db *mongo.Database) error {
	var recorded SchemaMetaSchema
	err := db.Collection(CollectionSchemaMeta).FindOne(ctx, bson.D{{Key: "_id", Value: SchemaMetaSchemaID}}).Decode(&recorded)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if recorded.Version < 2 {
		if err := removeTrackFrames(ctx, db.Collection(CollectionTracks)); err != nil {
			return fmt.Errorf("upgrade from version %d: %w", recorded.Version, err)
		}
	}
	return nil
}

// removeTrackFrames removes the frame counts that version 1 kept for each layer of a publication, both in
// publishStats and in the per-layer stats it is derived from. Anything else in those layers is left as it is: a failed
// update would stop the server.
func removeTrackFrames(ctx context.Context, tracks *mongo.Collection) error {
	if _, err := tracks.UpdateMany(ctx,
		bson.D{{Key: "publishStats.layers.frames", Value: bson.D{{Key: "$exists", Value: true}}}},
		bson.D{{Key: "$unset", Value: bson.D{{Key: "publishStats.layers.$[].frames", Value: ""}}}},
	); err != nil {
		return fmt.Errorf("publishStats: %w", err)
	}

	withoutFrames := bson.D{{Key: "$arrayToObject", Value: bson.D{{Key: "$filter", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$objectToArray", Value: "$$layer.v"}}},
		{Key: "as", Value: "field"},
		{Key: "cond", Value: bson.D{{Key: "$ne", Value: bson.A{"$$field.k", "frames"}}}},
	}}}}}
	update := mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "internal.publishStats", Value: bson.D{
		{Key: "$arrayToObject", Value: bson.D{{Key: "$map", Value: bson.D{
			{Key: "input", Value: bson.D{{Key: "$objectToArray", Value: "$internal.publishStats"}}},
			{Key: "as", Value: "layer"},
			{Key: "in", Value: bson.D{{Key: "k", Value: "$$layer.k"}, {Key: "v", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$eq", Value: bson.A{bson.D{{Key: "$type", Value: "$$layer.v"}}, "object"}}}, withoutFrames, "$$layer.v",
			}}}}}},
		}}}},
	}}}}}}
	if _, err := tracks.UpdateMany(ctx,
		bson.D{{Key: "internal.publishStats", Value: bson.D{{Key: "$type", Value: "object"}}}}, update); err != nil {
		return fmt.Errorf("internal.publishStats: %w", err)
	}
	return nil
}
