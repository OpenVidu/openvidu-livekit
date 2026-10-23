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

import "go.mongodb.org/mongo-driver/v2/bson"

// fields collects the assignments of one $set stage; setting a path again replaces its expression, since a stage
// cannot assign the same field twice.
type fields struct {
	order  []string
	values map[string]any
}

func newFields() *fields {
	return &fields{values: map[string]any{}}
}

func (f *fields) set(path string, value any) *fields {
	if _, ok := f.values[path]; !ok {
		f.order = append(f.order, path)
	}
	f.values[path] = value
	return f
}

func (f *fields) doc() bson.D {
	document := make(bson.D, 0, len(f.order))
	for _, path := range f.order {
		document = append(document, bson.E{Key: path, Value: f.values[path]})
	}
	return document
}
