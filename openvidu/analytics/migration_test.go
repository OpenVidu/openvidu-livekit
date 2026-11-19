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

package analytics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// countingLease counts its refreshes and fails the failAt-th one; failAt 0 never fails.
type countingLease struct {
	refreshes int
	failAt    int
}

func (l *countingLease) Refresh(context.Context, time.Duration) error {
	l.refreshes++
	if l.refreshes == l.failAt {
		return errors.New("lock expired")
	}
	return nil
}

func (l *countingLease) Release(context.Context) error { return nil }

func TestMigrationRetries(t *testing.T) {
	ctx := context.Background()
	newTestMigrator := func() *migrator {
		m := &migrator{backoff: time.Millisecond, log: defaultJobLog()}
		m.state.State = migrationRunning
		return m
	}

	t.Run("Transient error with progress", func(t *testing.T) {
		m := newTestMigrator()
		held := &countingLease{}
		calls := 0
		err := m.withRetries(ctx, held, func() error {
			calls++
			switch {
			case calls == 5:
				m.state.Processed += 50
				return networkError
			case calls < 9:
				return networkError
			}
			return nil
		})
		require.NoError(t, err, "the attempt count starts again after the progress")
		require.Equal(t, 9, calls)
		require.Equal(t, 8, held.refreshes, "the lock is refreshed before every retry")
		require.Equal(t, migrationRunning, m.state.State)
	})

	t.Run("Transient error with progress: without progress the phase gives up", func(t *testing.T) {
		m := newTestMigrator()
		calls := 0
		err := m.withRetries(ctx, &countingLease{}, func() error {
			calls++
			return networkError
		})
		require.Error(t, err)
		require.NotErrorIs(t, err, errLockLost)
		require.Equal(t, migrationRetries, calls)
	})

	t.Run("Lock refreshed while a batch retries", func(t *testing.T) {
		m := newTestMigrator()
		held := &countingLease{failAt: 2}
		attempts := 0
		err := m.retrying(ctx, held, func() error {
			attempts++
			return networkError
		})
		require.ErrorIs(t, err, errLockLost)
		require.Equal(t, 2, attempts, "no third attempt")
		require.Equal(t, 2, held.refreshes, "refreshed before the second attempt, failed before the third")
	})

	t.Run("Lock refreshed while a batch retries: between rounds", func(t *testing.T) {
		m := newTestMigrator()
		held := &countingLease{failAt: 1}
		rounds := 0
		err := m.withRetries(ctx, held, func() error {
			rounds++
			return networkError
		})
		require.ErrorIs(t, err, errLockLost)
		require.Equal(t, 1, rounds)
	})
}
