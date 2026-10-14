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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/protojson"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/proto"
)

func TestRecordedLegacyFixtures(t *testing.T) {
	counts := map[string]int{}
	for _, raw := range recordedLegacy(t, "events") {
		event, err := DecodeEvent(raw)
		require.NoError(t, err, "%s", raw)
		require.NotZero(t, event.Seconds)
		kind := "real"
		if event.Fake {
			kind = "fake"
			require.Empty(t, event.Message.GetId())
		} else {
			require.NotEmpty(t, event.Message.GetId())
		}
		counts[event.Message.GetType().String()+"/"+kind]++
	}
	stats := recordedLegacy(t, "stats")
	for _, raw := range stats {
		stat, err := DecodeStat(raw)
		require.NoError(t, err, "%s", raw)
		require.NotEmpty(t, stat.GetId())
		require.NotZero(t, Seconds(raw, "time_stamp"))
	}

	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t.Logf("%-45s %d", key, counts[key])
	}
	t.Logf("stats: %d, last_alive: %d", len(stats), len(recordedLegacy(t, "last_alive")))
	require.Equal(t, 6, counts["PARTICIPANT_LEFT/fake"])
	require.NotZero(t, counts["EGRESS_STARTED/real"])
	require.NotZero(t, counts["INGRESS_ENDED/real"])
}

func findRecorded(t *testing.T, collection string, match func(bson.Raw) bool) bson.Raw {
	t.Helper()
	for _, raw := range recordedLegacy(t, collection) {
		if match(raw) {
			return raw
		}
	}
	t.Fatalf("no recorded %s document matches", collection)
	return nil
}

func ofType(eventType string) func(bson.Raw) bool {
	return func(raw bson.Raw) bool {
		value, err := raw.LookupErr("type")
		return err == nil && value.StringValue() == eventType
	}
}

func TestDecodeRecordedShapes(t *testing.T) {
	t.Run("room created without room_id, numbers as doubles", func(t *testing.T) {
		raw := findRecorded(t, "events", ofType("ROOM_CREATED"))
		_, err := raw.LookupErr("room_id")
		require.Error(t, err, "the recorded ROOM_CREATED has no room_id")
		event, err := DecodeEvent(raw)
		require.NoError(t, err)
		require.Equal(t, event.Message.GetRoom().GetSid(), event.Message.GetRoomId())
		require.EqualValues(t, 300, event.Message.GetRoom().GetEmptyTimeout())
		require.NotZero(t, event.Message.GetRoom().GetCreationTimeMs())
		require.Equal(t, raw.Lookup("id").StringValue(), event.Message.GetId())
	})

	t.Run("egress with a Go-named oneof and nanosecond doubles", func(t *testing.T) {
		raw := findRecorded(t, "events", ofType("EGRESS_STARTED"))
		event, err := DecodeEvent(raw)
		require.NoError(t, err)
		egress := event.Message.GetEgress()
		require.NotNil(t, egress.GetRoomComposite(), "Request.RoomComposite maps to the room_composite oneof member")
		require.Equal(t, livekit.EgressStatus_EGRESS_STARTING, egress.GetStatus())
		recorded := raw.Lookup("egress", "started_at").Double()
		require.InDelta(t, recorded, float64(egress.GetStartedAt()), 1e6, "within 1 ms of the recorded value")
		require.Equal(t, livekit.EncodedFileType_DEFAULT_FILETYPE, egress.GetRoomComposite().GetFileOutputs()[0].GetFileType())
	})

	t.Run("enums by number", func(t *testing.T) {
		raw := findRecorded(t, "events", func(raw bson.Raw) bool {
			kind, err := raw.LookupErr("participant", "kind")
			return ofType("PARTICIPANT_JOINED")(raw) && err == nil && kind.Type == bson.TypeDouble
		})
		event, err := DecodeEvent(raw)
		require.NoError(t, err)
		require.EqualValues(t, raw.Lookup("participant", "kind").Double(), event.Message.GetParticipant().GetKind())
		require.Equal(t, livekit.ParticipantInfo_JOINED, event.Message.GetParticipant().GetState(),
			"the legacy writer stored the state by name")
	})

	t.Run("fixer event", func(t *testing.T) {
		raw := findRecorded(t, "events", func(raw bson.Raw) bool { return raw.Lookup("_id").Type == bson.TypeObjectID })
		event, err := DecodeEvent(raw)
		require.NoError(t, err)
		require.True(t, event.Fake)
		require.Empty(t, event.Message.GetId())
		require.Equal(t, raw.Lookup("_id").ObjectID().Hex(), event.LegacyID)
		require.Equal(t, livekit.AnalyticsEventType_PARTICIPANT_LEFT, event.Message.GetType())
		require.EqualValues(t, raw.Lookup("timestamp", "seconds").Int64(), event.Message.GetTimestamp().GetSeconds())
	})

	t.Run("event without LiveKit id", func(t *testing.T) {
		raw := findRecorded(t, "events", ofType("PARTICIPANT_ACTIVE"))
		var document bson.D
		require.NoError(t, bson.Unmarshal(raw, &document))
		withoutID := bson.D{}
		for _, element := range document {
			if element.Key != "id" {
				withoutID = append(withoutID, element)
			}
		}
		stripped, err := bson.Marshal(withoutID)
		require.NoError(t, err)
		first, err := DecodeEvent(stripped)
		require.NoError(t, err)
		second, err := DecodeEvent(stripped)
		require.NoError(t, err)
		require.False(t, first.Fake)
		require.Equal(t, "AE_LEG_"+raw.Lookup("_id").StringValue(), first.Message.GetId())
		require.Equal(t, first.Message.GetId(), second.Message.GetId())
	})

	t.Run("unknown type", func(t *testing.T) {
		raw, err := bson.Marshal(bson.D{{Key: "_id", Value: "abc"}, {Key: "id", Value: "AE_x"},
			{Key: "type", Value: "ROOM_EXPLODED"}, {Key: "timestamp", Value: bson.D{{Key: "seconds", Value: 1.7e9}}}})
		require.NoError(t, err)
		_, err = DecodeEvent(raw)
		var skip *SkipError
		require.True(t, errors.As(err, &skip))
		require.Equal(t, "abc", skip.ID)
	})

	t.Run("stat", func(t *testing.T) {
		raw := findRecorded(t, "stats", func(raw bson.Raw) bool { return true })
		stat, err := DecodeStat(raw)
		require.NoError(t, err)
		require.Equal(t, livekit.StreamType_UPSTREAM, stat.GetKind())
		require.EqualValues(t, raw.Lookup("time_stamp", "seconds").Double(), stat.GetTimeStamp().GetSeconds())
		require.EqualValues(t, raw.Lookup("streams", "0", "primary_bytes").Double(), stat.GetStreams()[0].GetPrimaryBytes())
	})
}

// TestDecodeLegacyEncodingOfEveryRecordedEvent encodes every recorded protocol fixture of the writer the way the
// legacy writer stored it and decodes it back.
func TestDecodeLegacyEncodingOfEveryRecordedEvent(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "testdata", "events", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
			original := &livekit.AnalyticsEvent{}
			readProtocolFixture(t, file, original)
			decoded, err := DecodeEvent(legacyEventDocument(t, original))
			require.NoError(t, err)
			expected := proto.Clone(original).(*livekit.AnalyticsEvent)
			if expected.GetType() == livekit.AnalyticsEventType_ROOM_CREATED {
				expected.RoomId = expected.GetRoom().GetSid()
			}
			requireEquivalent(t, expected, decoded.Message)
		})
	}

	statFiles, err := filepath.Glob(filepath.Join("..", "testdata", "stats", "*.json"))
	require.NoError(t, err)
	for _, file := range statFiles {
		flush := &livekit.AnalyticsStats{}
		readProtocolFixture(t, file, flush)
		for i, original := range flush.GetStats() {
			t.Run(fmt.Sprintf("%s/%d", strings.TrimSuffix(filepath.Base(file), ".json"), i), func(t *testing.T) {
				decoded, err := DecodeStat(legacyStatDocument(t, original))
				require.NoError(t, err)
				requireEquivalent(t, original, decoded)
			})
		}
	}
}

func readProtocolFixture(t *testing.T, file string, message proto.Message) {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NoError(t, protojson.Unmarshal(data, message))
}

// requireEquivalent compares two messages through protojson, allowing integers above 2^53 the error of the doubles
// the legacy writer stored them in.
func requireEquivalent(t *testing.T, expected proto.Message, actual proto.Message) {
	t.Helper()
	toTree := func(message proto.Message) any {
		data, err := protojson.Marshal(message)
		require.NoError(t, err)
		var tree any
		require.NoError(t, json.Unmarshal(data, &tree))
		return tree
	}
	for _, difference := range treeDifferences("", toTree(expected), toTree(actual)) {
		t.Error(difference)
	}
}

func treeDifferences(path string, expected any, actual any) []string {
	switch e := expected.(type) {
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: expected an object, got %v", path, actual)}
		}
		var differences []string
		for key, value := range e {
			differences = append(differences, treeDifferences(path+"."+key, value, a[key])...)
		}
		for key, value := range a {
			if _, ok := e[key]; !ok {
				differences = append(differences, fmt.Sprintf("%s.%s: unexpected %v", path, key, value))
			}
		}
		return differences
	case []any:
		a, ok := actual.([]any)
		if !ok || len(a) != len(e) {
			return []string{fmt.Sprintf("%s: expected %v, got %v", path, expected, actual)}
		}
		var differences []string
		for i := range e {
			differences = append(differences, treeDifferences(fmt.Sprintf("%s[%d]", path, i), e[i], a[i])...)
		}
		return differences
	case string:
		if a, ok := actual.(string); ok && closeLargeIntegers(e, a) {
			return nil
		}
	}
	if fmt.Sprint(expected) != fmt.Sprint(actual) {
		return []string{fmt.Sprintf("%s: expected %v, got %v", path, expected, actual)}
	}
	return nil
}

// closeLargeIntegers accepts int64 values (protojson strings) above 2^53 that differ by at most 1 µs.
func closeLargeIntegers(expected string, actual string) bool {
	if expected == actual {
		return true
	}
	e, errE := strconv.ParseInt(expected, 10, 64)
	a, errA := strconv.ParseInt(actual, 10, 64)
	if errE != nil || errA != nil || math.Abs(float64(e)) < 1<<53 {
		return false
	}
	return math.Abs(float64(e-a)) <= 1000
}
