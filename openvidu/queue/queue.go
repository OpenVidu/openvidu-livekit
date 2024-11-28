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
	"errors"
	"sync"
)

type Queue[T any] interface {
	Enqueue(T) error
	Dequeue() (T, error)
	Len() int
	Contains(T, func(T, T) bool) bool
}

var (
	ErrQueueEmpty = errors.New("queue empty")
	ErrQueueFull  = errors.New("queue full")
)

type SliceQueue[T any] struct {
	data  []T
	mutex sync.RWMutex
}

func NewSliceQueue[T any]() Queue[T] {
	return &SliceQueue[T]{}
}

// Len returns the number of elements in the queue.
func (q *SliceQueue[T]) Len() int {
	q.mutex.RLock()
	defer q.mutex.RUnlock()
	return len(q.data)
}

// Enqueue adds an element to the end of the queue.
func (q *SliceQueue[T]) Enqueue(value T) error {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.data = append(q.data, value)
	return nil
}

// Dequeue removes and returns the first element from the queue.
func (q *SliceQueue[T]) Dequeue() (T, error) {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if len(q.data) == 0 {
		var empty T
		return empty, ErrQueueEmpty
	}

	element := q.data[0]
	q.data = q.data[1:]
	return element, nil
}

// Contains checks if an element exists in the queue based on the given equality function.
func (q *SliceQueue[T]) Contains(element T, equals func(T, T) bool) bool {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	for _, e := range q.data {
		if equals(e, element) {
			return true
		}
	}
	
	return false
}
