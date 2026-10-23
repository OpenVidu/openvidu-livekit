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

// Package schema holds the analytics data model contract shared with OpenVidu Dashboard: the vendored validators,
// index list and fixtures under contract/ (generated in openvidu-dashboard/dashboard-ce/typings, never edited here),
// the document types, the database initialization and the helpers that turn protocol messages into stored values.
package schema

import (
	"embed"
	"fmt"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// SchemaVersion is the version of the data model contract this writer implements. It must equal
// contract/schema/contract_version.txt.
const SchemaVersion = 1

//go:embed contract
var contractFS embed.FS

// Collection names of the data model.
const (
	CollectionRooms              = "rooms"
	CollectionParticipants       = "participants"
	CollectionTracks             = "tracks"
	CollectionTrackSubscriptions = "track_subscriptions"
	CollectionEgresses           = "egresses"
	CollectionIngresses          = "ingresses"
	CollectionIngressSessions    = "ingress_sessions"
	CollectionEvents             = "events"
	CollectionTrackStats         = "track_stats"
	CollectionAnalytics5Min      = "analytics_5min"
	CollectionAnalyticsHourly    = "analytics_hourly"
	CollectionAnalyticsDaily     = "analytics_daily"
	CollectionNodes              = "nodes"
	CollectionSchemaMeta         = "schema_meta"
)

// CollectionDefinition is an entry of contract/schema/collections.json.
type CollectionDefinition struct {
	Name           string                `bson:"name"`
	Schema         string                `bson:"schema"`
	RetentionClass RetentionClass        `bson:"retentionClass,omitempty"`
	Timeseries     *TimeseriesDefinition `bson:"timeseries,omitempty"`
}

// TimeseriesDefinition holds the time-series options of a collection.
type TimeseriesDefinition struct {
	TimeField      string         `bson:"timeField"`
	MetaField      string         `bson:"metaField"`
	Granularity    string         `bson:"granularity"`
	RetentionClass RetentionClass `bson:"retentionClass"`
}

// IndexDefinition is an entry of contract/schema/indexes.json. A TTL index has either a fixed
// ExpireAfterSeconds or a RetentionClass whose configured value becomes its expireAfterSeconds.
type IndexDefinition struct {
	Collection              string
	Name                    string
	Key                     bson.D
	Unique                  bool
	ExpireAfterSeconds      *int32
	PartialFilterExpression bson.D
	RetentionClass          RetentionClass
	TimeseriesFallbackOnly  bool
}

type indexEntry struct {
	Collection             string         `bson:"collection"`
	Name                   string         `bson:"name"`
	Key                    []bson.A       `bson:"key"`
	Options                indexOptions   `bson:"options"`
	RetentionClass         RetentionClass `bson:"retentionClass,omitempty"`
	TimeseriesFallbackOnly bool           `bson:"timeseriesFallbackOnly,omitempty"`
}

type indexOptions struct {
	Unique                  bool   `bson:"unique,omitempty"`
	ExpireAfterSeconds      *int32 `bson:"expireAfterSeconds,omitempty"`
	PartialFilterExpression bson.D `bson:"partialFilterExpression,omitempty"`
}

// ContractVersion returns the schema version recorded in the vendored contract.
func ContractVersion() (int, error) {
	data, err := contractFS.ReadFile("contract/schema/contract_version.txt")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// Collections returns every collection of the contract in creation order.
func Collections() ([]CollectionDefinition, error) {
	var collections []CollectionDefinition
	if err := readJSONArray("contract/schema/collections.json", &collections); err != nil {
		return nil, err
	}
	return collections, nil
}

// Indexes returns every index of the contract.
func Indexes() ([]IndexDefinition, error) {
	var entries []indexEntry
	if err := readJSONArray("contract/schema/indexes.json", &entries); err != nil {
		return nil, err
	}

	indexes := make([]IndexDefinition, 0, len(entries))
	for _, entry := range entries {
		key := make(bson.D, 0, len(entry.Key))
		for _, part := range entry.Key {
			if len(part) != 2 {
				return nil, fmt.Errorf("index %s.%s: malformed key %v", entry.Collection, entry.Name, part)
			}
			field, ok := part[0].(string)
			if !ok {
				return nil, fmt.Errorf("index %s.%s: key field %v is not a string", entry.Collection, entry.Name, part[0])
			}
			key = append(key, bson.E{Key: field, Value: part[1]})
		}
		indexes = append(indexes, IndexDefinition{
			Collection:              entry.Collection,
			Name:                    entry.Name,
			Key:                     key,
			Unique:                  entry.Options.Unique,
			ExpireAfterSeconds:      entry.Options.ExpireAfterSeconds,
			PartialFilterExpression: entry.Options.PartialFilterExpression,
			RetentionClass:          entry.RetentionClass,
			TimeseriesFallbackOnly:  entry.TimeseriesFallbackOnly,
		})
	}
	return indexes, nil
}

// Validator returns the `$jsonSchema` validator document of a collection, with the key order of the contract.
func Validator(definition CollectionDefinition) (bson.D, error) {
	data, err := contractFS.ReadFile("contract/schema/" + definition.Schema)
	if err != nil {
		return nil, err
	}
	var schema bson.D
	if err := bson.UnmarshalExtJSON(data, false, &schema); err != nil {
		return nil, fmt.Errorf("%s: %w", definition.Schema, err)
	}
	return bson.D{{Key: "$jsonSchema", Value: schema}}, nil
}

// Fixtures returns the raw canonical documents of a collection.
func Fixtures(collection string) ([]bson.Raw, error) {
	var documents []bson.Raw
	if err := readJSONArray("contract/fixtures/"+collection+".json", &documents); err != nil {
		return nil, err
	}
	return documents, nil
}

// readJSONArray decodes an Extended JSON array file, which the BSON decoder only accepts inside a document.
func readJSONArray(path string, target any) error {
	data, err := contractFS.ReadFile(path)
	if err != nil {
		return err
	}
	wrapped := make([]byte, 0, len(data)+12)
	wrapped = append(wrapped, `{"items":`...)
	wrapped = append(wrapped, data...)
	wrapped = append(wrapped, '}')

	var holder struct {
		Items bson.RawValue `bson:"items"`
	}
	if err := bson.UnmarshalExtJSON(wrapped, false, &holder); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := holder.Items.Unmarshal(target); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
