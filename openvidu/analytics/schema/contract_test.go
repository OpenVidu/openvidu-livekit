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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContractVersionMatchesSchemaVersion(t *testing.T) {
	version, err := ContractVersion()
	require.NoError(t, err)
	require.Equalf(t, SchemaVersion, version,
		"vendored contract version %d differs from SchemaVersion %d: re-vendor the contract or update the constant",
		version, SchemaVersion)
}

func TestContractFilesParse(t *testing.T) {
	collections, err := Collections()
	require.NoError(t, err)
	require.Len(t, collections, 14)

	names := map[string]bool{}
	for _, collection := range collections {
		names[collection.Name] = true

		validator, err := Validator(collection)
		require.NoError(t, err, collection.Name)
		require.Equal(t, "$jsonSchema", validator[0].Key)

		fixtures, err := Fixtures(collection.Name)
		require.NoError(t, err, collection.Name)
		require.NotEmpty(t, fixtures, collection.Name)
	}

	indexes, err := Indexes()
	require.NoError(t, err)
	require.NotEmpty(t, indexes)
	for _, index := range indexes {
		require.True(t, names[index.Collection], "%s.%s targets an unknown collection", index.Collection, index.Name)
		require.NotEmpty(t, index.Key, "%s.%s has no key", index.Collection, index.Name)
		require.False(t, index.ExpireAfterSeconds != nil && index.RetentionClass != "",
			"%s.%s combines a fixed TTL with a retention class", index.Collection, index.Name)
	}
}

func TestRetentionOf(t *testing.T) {
	retention := DefaultRetention()

	events, ok, err := retention.Of(RetentionEvents)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 7*day, events)

	_, ok, err = retention.Of(RetentionRollupsDaily)
	require.NoError(t, err)
	require.False(t, ok, "daily rollups are kept forever by default")

	_, _, err = retention.Of("weekly")
	require.Error(t, err)
}
