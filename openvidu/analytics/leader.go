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
	"sync"
	"time"

	"github.com/bsm/redislock"
	"github.com/livekit/protocol/logger"
)

const (
	// lockMargin is added to a job's interval for its lock TTL and kept free at the end of a run's time budget.
	lockMargin = 5 * time.Second
	// lockErrorPause spaces the attempts to obtain a lock while Redis fails.
	lockErrorPause = 5 * time.Second
)

// locker elects the node that runs a job.
type locker interface {
	// Obtain returns a held lease, or an error wrapping redislock.ErrNotObtained when another node holds it.
	Obtain(ctx context.Context, key string, ttl time.Duration) (lease, error)
}

type lease interface {
	Refresh(ctx context.Context, ttl time.Duration) error
	Release(ctx context.Context) error
}

// redisLocker obtains the lock in Redis, retrying every backoff until its TTL elapses, or once without backoff.
type redisLocker struct {
	client  *redislock.Client
	backoff time.Duration
}

func (l redisLocker) Obtain(ctx context.Context, key string, ttl time.Duration) (lease, error) {
	retry := redislock.NoRetry()
	if l.backoff > 0 {
		retry = redislock.LinearBackoff(l.backoff)
	}
	lock, err := l.client.Obtain(ctx, key, ttl, &redislock.Options{RetryStrategy: retry})
	if err != nil {
		return nil, err
	}
	return redisLease{lock}, nil
}

type redisLease struct {
	lock *redislock.Lock
}

func (l redisLease) Refresh(ctx context.Context, ttl time.Duration) error {
	return l.lock.Refresh(ctx, ttl, nil)
}

func (l redisLease) Release(ctx context.Context) error {
	return l.lock.Release(ctx)
}

// localLocker elects the only node of a deployment without Redis.
type localLocker struct {
	mu sync.Mutex
}

func (l *localLocker) Obtain(context.Context, string, time.Duration) (lease, error) {
	if !l.mu.TryLock() {
		return nil, redislock.ErrNotObtained
	}
	return localLease{&l.mu}, nil
}

type localLease struct {
	mu *sync.Mutex
}

func (localLease) Refresh(context.Context, time.Duration) error {
	return nil
}

func (l localLease) Release(context.Context) error {
	l.mu.Unlock()
	return nil
}

// jobLog writes the log lines of a job.
type jobLog struct {
	debugw func(msg string, keysAndValues ...any)
	infow  func(msg string, keysAndValues ...any)
	warnw  func(msg string, err error, keysAndValues ...any)
}

func defaultJobLog() jobLog {
	return jobLog{debugw: logger.Debugw, infow: logger.Infow, warnw: logger.Warnw}
}

// leaderJob runs a periodic job on one node at a time, under the lock key.
type leaderJob struct {
	// name prefixes the log lines.
	name           string
	key            string
	interval       time.Duration
	ttl            time.Duration
	lockErrorPause time.Duration
	log            jobLog
	// cycle runs the job once; it may refresh held with ttl to keep running.
	cycle func(ctx context.Context, held lease, ttl time.Duration)

	// releaseFailed silences repeated warnings while the lock cannot be released.
	releaseFailed bool
}

// loop runs cycles until ctx is cancelled: once right after obtaining the lock for the first time, then every interval
// while holding it.
func (j *leaderJob) loop(ctx context.Context, locks locker) {
	startup := true
	lockFailing := false
	for ctx.Err() == nil {
		held, err := locks.Obtain(ctx, j.key, j.ttl)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !errors.Is(err, redislock.ErrNotObtained) {
				if !lockFailing {
					j.log.warnw(j.name+": cannot obtain the lock, retrying", err, "pause", j.lockErrorPause)
					lockFailing = true
				}
				sleep(ctx, j.lockErrorPause)
			}
			continue
		}
		lockFailing = false
		if !startup {
			if !sleep(ctx, j.interval) || held.Refresh(ctx, j.ttl) != nil {
				j.release(held)
				continue
			}
		}
		startup = false
		j.cycle(ctx, held, j.ttl)
		j.release(held)
	}
}

// release frees the lock; after a Redis restart the lock is gone, which is harmless.
func (j *leaderJob) release(held lease) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := held.Release(ctx); err != nil {
		if !j.releaseFailed {
			j.log.infow(j.name+": lock not released, it expires with its TTL", "error", err.Error())
			j.releaseFailed = true
		}
		return
	}
	j.releaseFailed = false
}

// sleep waits for d and reports false when ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
