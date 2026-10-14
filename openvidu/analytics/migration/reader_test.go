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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type memorySource struct {
	documents []bson.Raw
}

func (s *memorySource) Next(context.Context) (bson.Raw, bool, error) {
	if len(s.documents) == 0 {
		return nil, false, nil
	}
	document := s.documents[0]
	s.documents = s.documents[1:]
	return document, true, nil
}

func legacyAt(t *testing.T, seconds float64, nanos float64) bson.Raw {
	raw, err := bson.Marshal(bson.D{{Key: "_id", Value: bson.NewObjectID().Hex()},
		{Key: EventTimestamp, Value: bson.D{{Key: "seconds", Value: seconds}, {Key: "nanos", Value: nanos}}}})
	require.NoError(t, err)
	return raw
}

func TestReaderKeepsWholeSeconds(t *testing.T) {
	var documents []bson.Raw
	for i := range 100 {
		documents = append(documents, legacyAt(t, 1000, float64(1000-i)))
	}
	for i := range 700 {
		documents = append(documents, legacyAt(t, 1001, float64(i)))
	}
	for i := range 10 {
		documents = append(documents, legacyAt(t, 1002, float64(i)))
	}
	reader := NewReader(&memorySource{documents: documents}, EventTimestamp, 500)

	var sizes []int
	var seconds []int64
	for {
		batch, err := reader.Next(context.Background())
		require.NoError(t, err)
		if len(batch.Documents) == 0 {
			break
		}
		sizes = append(sizes, len(batch.Documents))
		seconds = append(seconds, batch.Second)
		for i := 1; i < len(batch.Documents); i++ {
			previous, current := batch.Documents[i-1].Lookup(EventTimestamp), batch.Documents[i].Lookup(EventTimestamp)
			ps, cs := previous.Document().Lookup("seconds").Double(), current.Document().Lookup("seconds").Double()
			require.True(t, ps < cs || (ps == cs && previous.Document().Lookup("nanos").Double() <= current.Document().Lookup("nanos").Double()),
				"ordered by second and nanos")
		}
	}
	require.Equal(t, []int{800, 10}, sizes, "the 700 documents of second 1001 stay in one batch")
	require.Equal(t, []int64{1001, 1002}, seconds)
}

func TestReaderSmallBatches(t *testing.T) {
	var documents []bson.Raw
	for second := range 5 {
		for i := range 3 {
			documents = append(documents, legacyAt(t, float64(2000+second), float64(i)))
		}
	}
	reader := NewReader(&memorySource{documents: documents}, EventTimestamp, 4)
	var sizes []int
	for {
		batch, err := reader.Next(context.Background())
		require.NoError(t, err)
		if len(batch.Documents) == 0 {
			break
		}
		sizes = append(sizes, len(batch.Documents))
	}
	require.Equal(t, []int{6, 6, 3}, sizes)
}

func TestPause(t *testing.T) {
	require.Equal(t, time.Duration(0), Pause(500, 0, 0), "rate 0 is unthrottled")
	require.Equal(t, 500*time.Millisecond, Pause(500, 1000, 0))
	require.Equal(t, 300*time.Millisecond, Pause(500, 1000, 200*time.Millisecond))
	require.Equal(t, time.Duration(0), Pause(500, 1000, time.Second))

	var total time.Duration
	for range 20 {
		total += Pause(500, 1000, 0)
	}
	require.GreaterOrEqual(t, total, 10*time.Second, "10 000 documents at rate 1000 take at least 10 s")
}
