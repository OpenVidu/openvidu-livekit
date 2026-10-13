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

package projection

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Aggregation expressions used by the update pipelines. Each helper reads the document as it is before the stage
// that contains the expression.

func field(path string) string {
	return "$" + path
}

func ifNull(path string, fallback any) bson.D {
	return bson.D{{Key: "$ifNull", Value: bson.A{field(path), fallback}}}
}

// keep returns the current value of path, or value when path is missing or null.
func keep(path string, value any) bson.D {
	return ifNull(path, value)
}

// minDate keeps the earliest of the current value and t. $min ignores a missing operand.
func minDate(path string, t time.Time) bson.D {
	return bson.D{{Key: "$min", Value: bson.A{field(path), t}}}
}

// maxDate keeps the latest of the current value and t.
func maxDate(path string, t time.Time) bson.D {
	return bson.D{{Key: "$max", Value: bson.A{field(path), t}}}
}

func emptyArray(path string) bson.D {
	return ifNull(path, bson.A{})
}

// union adds values to the set stored at path.
func union(path string, values ...any) bson.D {
	return bson.D{{Key: "$setUnion", Value: bson.A{emptyArray(path), bson.A(values)}}}
}

func size(expression any) bson.D {
	return bson.D{{Key: "$size", Value: expression}}
}

func sizeOf(path string) bson.D {
	return size(emptyArray(path))
}

func difference(a any, b any) bson.D {
	return bson.D{{Key: "$setDifference", Value: bson.A{a, b}}}
}

// sizesOf turns a document of sets ({KEY: [ids]}) into a document of their sizes ({KEY: n}); keys without ids are
// left out.
func sizesOf(path string) bson.D {
	return bson.D{{Key: "$arrayToObject", Value: bson.D{{Key: "$map", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$filter", Value: bson.D{
			{Key: "input", Value: bson.D{{Key: "$objectToArray", Value: ifNull(path, bson.D{})}}},
			{Key: "cond", Value: bson.D{{Key: "$gt", Value: bson.A{size("$$this.v"), 0}}}},
		}}}},
		{Key: "in", Value: bson.D{{Key: "k", Value: "$$this.k"}, {Key: "v", Value: size("$$this.v")}}},
	}}}}}
}

func cond(condition any, then any, otherwise any) bson.D {
	return bson.D{{Key: "$cond", Value: bson.A{condition, then, otherwise}}}
}

func eq(a any, b any) bson.D {
	return bson.D{{Key: "$eq", Value: bson.A{a, b}}}
}

func gt(a any, b any) bson.D {
	return bson.D{{Key: "$gt", Value: bson.A{a, b}}}
}

func gte(a any, b any) bson.D {
	return bson.D{{Key: "$gte", Value: bson.A{a, b}}}
}

func in(value any, values bson.A) bson.D {
	return bson.D{{Key: "$in", Value: bson.A{value, values}}}
}

func add(values ...any) bson.D {
	return bson.D{{Key: "$add", Value: bson.A(values)}}
}

func subtract(a any, b any) bson.D {
	return bson.D{{Key: "$subtract", Value: bson.A{a, b}}}
}

func exists(path string) bson.D {
	return gt(field(path), nil)
}

// remove evaluates to the special variable that drops a field from the output of $set.
const remove = "$$REMOVE"

// rank orders the statuses of a lifecycle; statuses of equal rank (terminal ones) do not replace each other.
type rank struct {
	status string
	order  int
}

// ranked sets path to value unless the current value has a higher or equal rank, so a late event never regresses
// a status and the first of two terminal statuses wins.
func ranked(path string, value string, ranks []rank) bson.D {
	branches := bson.A{}
	target := -1
	for _, r := range ranks {
		branches = append(branches, bson.D{{Key: "case", Value: eq(field(path), r.status)}, {Key: "then", Value: r.order}})
		if r.status == value {
			target = r.order
		}
	}
	current := bson.D{{Key: "$switch", Value: bson.D{{Key: "branches", Value: branches}, {Key: "default", Value: -1}}}}
	return cond(gt(target, current), value, field(path))
}

// latest sets path to value when at is not older than the instant stored at atPath (last writer wins by event time).
func latest(path string, atPath string, at time.Time, value any) bson.D {
	return cond(gte(at, ifNull(atPath, time.Unix(0, 0).UTC())), value, field(path))
}

// stage builds a {$set: fields} pipeline stage.
func stage(fields bson.D) bson.D {
	return bson.D{{Key: "$set", Value: fields}}
}

// maxOf keeps the highest of the number at path (0 when missing) and value.
func maxOf(path string, value any) bson.D {
	return bson.D{{Key: "$max", Value: bson.A{ifNull(path, int32(0)), value}}}
}

func or(conditions ...any) bson.D {
	return bson.D{{Key: "$or", Value: bson.A(conditions)}}
}

func and(conditions ...any) bson.D {
	return bson.D{{Key: "$and", Value: bson.A(conditions)}}
}

// minNumber keeps the lowest of the number at path and value; a missing number takes value.
func minNumber(path string, value float64) bson.D {
	return bson.D{{Key: "$min", Value: bson.A{field(path), value}}}
}
