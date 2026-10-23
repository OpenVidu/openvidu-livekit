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
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Timestamp fields the legacy documents are ordered by.
const (
	EventTimestamp = "timestamp"
	StatTimestamp  = "time_stamp"
)

// Source yields documents in ascending order of their timestamp second; ok is false at the end.
type Source interface {
	Next(ctx context.Context) (document bson.Raw, ok bool, err error)
}

// Batch is a set of documents that ends with a whole second.
type Batch struct {
	Documents []bson.Raw
	// Second is the last timestamp second of the batch: once the batch is applied, the replay resumes after it.
	Second int64
}

// Reader reads a Source in batches of at least size documents (fewer at the end), extended until the second of their
// last document is complete, so that a checkpoint on whole seconds never reapplies nor skips a document.
type Reader struct {
	source  Source
	field   string
	size    int
	pending bson.Raw
	done    bool
}

// NewReader reads source in batches of size, ordering the documents by field (EventTimestamp or StatTimestamp).
func NewReader(source Source, field string, size int) *Reader {
	return &Reader{source: source, field: field, size: size}
}

// Next returns the next batch, or a batch without documents at the end.
func (r *Reader) Next(ctx context.Context) (Batch, error) {
	var batch Batch
	if r.pending != nil {
		batch.Documents = append(batch.Documents, r.pending)
		batch.Second = Seconds(r.pending, r.field)
		r.pending = nil
	}
	for !r.done {
		document, ok, err := r.source.Next(ctx)
		if err != nil {
			return Batch{}, err
		}
		if !ok {
			r.done = true
			break
		}
		second := Seconds(document, r.field)
		if len(batch.Documents) >= r.size && second != batch.Second {
			r.pending = document
			break
		}
		batch.Documents = append(batch.Documents, document)
		batch.Second = second
	}
	nanos := func(i int) int64 {
		value, err := batch.Documents[i].LookupErr(r.field, "nanos")
		if err != nil {
			return 0
		}
		n, _ := number(value)
		return int64(n)
	}
	sort.SliceStable(batch.Documents, func(i, j int) bool {
		si, sj := Seconds(batch.Documents[i], r.field), Seconds(batch.Documents[j], r.field)
		if si != sj {
			return si < sj
		}
		return nanos(i) < nanos(j)
	})
	return batch, nil
}

// Pause is how long to wait after a batch of n documents applied in elapsed so that the replay stays under rate
// documents per second; rate 0 is unthrottled.
func Pause(n int, rate int, elapsed time.Duration) time.Duration {
	if rate <= 0 {
		return 0
	}
	return max(time.Duration(float64(n)/float64(rate)*float64(time.Second))-elapsed, 0)
}

// CursorSource reads a legacy collection after a checkpoint second, in ascending order of field, through a cursor that
// holds one batch of size documents.
type CursorSource struct {
	cursor *mongo.Cursor
}

// OpenCursor starts reading collection after the second checkpoint.
func OpenCursor(ctx context.Context, collection *mongo.Collection, field string, checkpoint int64, size int) (*CursorSource, error) {
	cursor, err := collection.Find(ctx, CursorFilter(field, checkpoint),
		options.Find().SetSort(bson.D{{Key: field + ".seconds", Value: 1}}).SetBatchSize(int32(size)))
	if err != nil {
		return nil, err
	}
	return &CursorSource{cursor: cursor}, nil
}

// CursorFilter selects the documents after the checkpoint second.
func CursorFilter(field string, checkpoint int64) bson.D {
	return bson.D{{Key: field + ".seconds", Value: bson.D{{Key: "$gt", Value: checkpoint}}}}
}

// Next copies the current document, which the cursor reuses.
func (s *CursorSource) Next(ctx context.Context) (bson.Raw, bool, error) {
	if !s.cursor.Next(ctx) {
		return nil, false, s.cursor.Err()
	}
	return append(bson.Raw(nil), s.cursor.Current...), true, nil
}

// Close releases the cursor.
func (s *CursorSource) Close(ctx context.Context) error {
	return s.cursor.Close(ctx)
}
