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

package migration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// legacyFixtureCollections are exported in natural order, which keeps the insertion order of the legacy writer.
var legacyFixtureCollections = []string{"events", "stats", "last_alive", "active_entities"}

// secretKeys are replaced by placeholders on export, so that the fixtures keep the fields the allow-list must drop
// without their recorded values.
var secretKeys = map[string]string{
	"turn_password": "SECRET-turn",
	"stream_key":    "SECRET-streamkey",
	"access_key":    "SECRET-access",
	"secret":        "SECRET-secret",
	"session_token": "SECRET-token",
}

// TestExportLegacyFixtures re-records testdata/legacy from a database written by the legacy writer, in canonical
// Extended JSON, one document per line. It runs only when MIGRATION_LEGACY_SOURCE_URI and
// MIGRATION_LEGACY_SOURCE_DB name that database.
func TestExportLegacyFixtures(t *testing.T) {
	uri, database := os.Getenv("MIGRATION_LEGACY_SOURCE_URI"), os.Getenv("MIGRATION_LEGACY_SOURCE_DB")
	if uri == "" || database == "" {
		t.Skip("set MIGRATION_LEGACY_SOURCE_URI and MIGRATION_LEGACY_SOURCE_DB to a legacy analytics database")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })

	for _, collection := range legacyFixtureCollections {
		cursor, err := client.Database(database).Collection(collection).Find(ctx, bson.D{})
		require.NoError(t, err)
		var lines []string
		for cursor.Next(ctx) {
			var document bson.D
			require.NoError(t, bson.Unmarshal(cursor.Current, &document))
			line, err := bson.MarshalExtJSON(redact(document), true, false)
			require.NoError(t, err)
			lines = append(lines, string(line))
		}
		require.NoError(t, cursor.Err())
		content := strings.Join(lines, "\n")
		if content != "" {
			content += "\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join("testdata", "legacy", collection+".json"), []byte(content), 0o644))
		t.Logf("%s: %d documents", collection, len(lines))
	}
}

func redact(document bson.D) bson.D {
	out := make(bson.D, len(document))
	for i, element := range document {
		if placeholder, ok := secretKeys[element.Key]; ok {
			if value, isString := element.Value.(string); isString && value != "" {
				element.Value = placeholder
			}
		}
		element.Value = redactValue(element.Value)
		out[i] = element
	}
	return out
}

func redactValue(value any) any {
	switch v := value.(type) {
	case bson.D:
		return redact(v)
	case bson.A:
		out := make(bson.A, len(v))
		for i, item := range v {
			out[i] = redactValue(item)
		}
		return out
	default:
		return value
	}
}
