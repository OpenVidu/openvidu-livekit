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
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// newDocument returns an empty document struct for a fixture of the collection.
func newDocument(t *testing.T, collection string, raw bson.Raw) any {
	switch collection {
	case CollectionRooms:
		return &RoomDocument{}
	case CollectionParticipants:
		return &ParticipantDocument{}
	case CollectionTracks:
		return &TrackDocument{}
	case CollectionTrackSubscriptions:
		return &TrackSubscriptionDocument{}
	case CollectionEgresses:
		return &EgressDocument{}
	case CollectionIngresses:
		return &IngressDocument{}
	case CollectionIngressSessions:
		return &IngressSessionDocument{}
	case CollectionEvents:
		return &EventDocument{}
	case CollectionTrackStats:
		return &TrackStatsDocument{}
	case CollectionAnalytics5Min:
		return &FiveMinuteRollupDocument{}
	case CollectionAnalyticsHourly:
		return &HourlyRollupDocument{}
	case CollectionAnalyticsDaily:
		return &DailyRollupDocument{}
	case CollectionNodes:
		return &NodeDocument{}
	case CollectionSchemaMeta:
		switch raw.Lookup("_id").StringValue() {
		case SchemaMetaSchemaID:
			return &SchemaMetaSchema{}
		case SchemaMetaRetentionID:
			return &SchemaMetaRetention{}
		case SchemaMetaCapabilitiesID:
			return &SchemaMetaCapabilities{}
		case SchemaMetaConfigID:
			return &SchemaMetaConfig{}
		case SchemaMetaMigrationID:
			return &SchemaMetaMigration{}
		case SchemaMetaReconcilerID:
			return &SchemaMetaReconciler{}
		}
	}
	t.Fatalf("no document type for %s %s", collection, raw.Lookup("_id"))
	return nil
}

func TestFixturesRoundTripThroughDocumentTypes(t *testing.T) {
	collections, err := Collections()
	require.NoError(t, err)

	for _, collection := range collections {
		fixtures, err := Fixtures(collection.Name)
		require.NoError(t, err)

		for i, raw := range fixtures {
			name := fmt.Sprintf("%s[%d]", collection.Name, i)
			t.Run(name, func(t *testing.T) {
				document := newDocument(t, collection.Name, raw)
				require.NoError(t, bson.Unmarshal(raw, document))

				encoded, err := bson.Marshal(document)
				require.NoError(t, err)
				require.Equal(t, canonical(raw), canonical(bson.Raw(encoded)))
			})
		}
	}
}

// canonical renders a BSON value with sorted document keys and explicit types, so two documents compare equal when
// they hold the same fields with the same BSON types and values regardless of field order.
func canonical(value any) string {
	switch v := value.(type) {
	case bson.Raw:
		elements, err := v.Elements()
		if err != nil {
			return "invalid: " + err.Error()
		}
		parts := make([]string, 0, len(elements))
		for _, element := range elements {
			parts = append(parts, element.Key()+"="+canonical(element.Value()))
		}
		sort.Strings(parts)
		return fmt.Sprintf("{%v}", parts)
	case bson.RawValue:
		switch v.Type {
		case bson.TypeEmbeddedDocument:
			return canonical(v.Document())
		case bson.TypeArray:
			values, err := v.Array().Values()
			if err != nil {
				return "invalid: " + err.Error()
			}
			parts := make([]string, 0, len(values))
			for _, item := range values {
				parts = append(parts, canonical(item))
			}
			return fmt.Sprintf("%v", parts)
		default:
			return fmt.Sprintf("%s:%s", v.Type, v.String())
		}
	}
	return fmt.Sprintf("unexpected %T", value)
}
