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

package queue

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRingKeepsOrder(t *testing.T) {
	r := NewRing[int](4)
	for i := 1; i <= 3; i++ {
		_, dropped := r.Push(i)
		require.False(t, dropped)
	}
	require.Equal(t, 3, r.Len())
	require.Equal(t, []int{1, 2}, r.Drain(2))
	require.Equal(t, []int{3}, r.Drain(0))
	require.Empty(t, r.Drain(0))
}

func TestRingEvictsOldestWhenFull(t *testing.T) {
	r := NewRing[int](100)
	dropped := 0
	for i := 1; i <= 150; i++ {
		if evicted, ok := r.Push(i); ok {
			dropped++
			require.Equal(t, dropped, evicted, "the oldest element is evicted")
		}
	}
	require.Equal(t, 50, dropped)
	require.Equal(t, 100, r.Len())

	drained := r.Drain(0)
	require.Equal(t, 51, drained[0])
	require.Equal(t, 150, drained[99])
}

func TestRingRequeueGoesAhead(t *testing.T) {
	r := NewRing[int](5)
	for i := 1; i <= 3; i++ {
		r.Push(i)
	}
	batch := r.Drain(0)
	r.Push(4)
	r.Push(5)

	require.Zero(t, r.Requeue(batch))
	require.Equal(t, []int{1, 2, 3, 4, 5}, r.Drain(0))
}

func TestRingRequeueDropsOldestThatDoNotFit(t *testing.T) {
	r := NewRing[int](4)
	r.Push(1)
	r.Push(2)
	r.Push(3)
	batch := r.Drain(0)
	r.Push(4)
	r.Push(5)

	require.Equal(t, 1, r.Requeue(batch))
	require.Equal(t, []int{2, 3, 4, 5}, r.Drain(0))
}

func TestRingConcurrentPushNeverBlocks(t *testing.T) {
	r := NewRing[int](1000)
	var wg sync.WaitGroup
	for producer := 0; producer < 8; producer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				r.Push(i)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1000, r.Len())
}
