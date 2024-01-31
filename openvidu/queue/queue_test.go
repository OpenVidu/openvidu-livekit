// Copyright 2024 OpenVidu
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

package queue

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSliceQueue(t *testing.T) {
	q := &SliceQueue[int]{}
	assert.Equal(t, 0, q.Len())

	err := q.Enqueue(1)
	assert.NoError(t, err)
	assert.Equal(t, 1, q.Len())

	err = q.Enqueue(2)
	assert.NoError(t, err)
	assert.Equal(t, 2, q.Len())

	value, err := q.Dequeue()
	assert.NoError(t, err)
	assert.Equal(t, 1, value)
	assert.Equal(t, 1, q.Len())

	value, err = q.Dequeue()
	assert.NoError(t, err)
	assert.Equal(t, 2, value)
	assert.Equal(t, 0, q.Len())

	_, err = q.Dequeue()
	assert.Error(t, err)
}
