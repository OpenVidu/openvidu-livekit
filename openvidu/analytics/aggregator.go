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
	"slices"
	"time"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
)

// The aggregator writes the analytics values that the writer of each server cannot compute from its own events: the
// peaks of rooms and participant sessions active at once, and the daily unique identities. One node at a time runs it,
// every minute, under a lock of its own.

const (
	aggregatorLockKey = "analytics-aggregator-lock"
	// aggregatorInterval is the period of the runs, and so of the samples of the peaks.
	aggregatorInterval = time.Minute
	// uniqueIdentityDaysPerRun bounds the days whose unique identities one run computes.
	uniqueIdentityDaysPerRun = 7
	// dayCloseGrace leaves time for the last batches of a UTC day before its unique identities are computed.
	dayCloseGrace = 5 * time.Minute
)

// aggregatorStore is what the aggregator needs from the database; *store.Store implements it.
type aggregatorStore interface {
	ActiveRooms(ctx context.Context) (store.ActiveCount, error)
	ActiveParticipants(ctx context.Context) (store.ActiveCount, error)
	ActiveEgress(ctx context.Context) (int32, error)
	ActiveIngressSessions(ctx context.Context) (int32, error)
	LiveMediaNodes(ctx context.Context, now time.Time) ([]string, error)
	ApplyIncrements(ctx context.Context, increments []projection.Increment) error
	DaysWithoutUniqueIdentities(ctx context.Context, from time.Time, to time.Time, limit int) ([]time.Time, error)
	UniqueIdentities(ctx context.Context, from time.Time, to time.Time) (int32, map[string]int32, error)
	SetUniqueIdentities(ctx context.Context, day time.Time, global int32, perNode map[string]int32) error
}

// aggregatorSettings holds the schedule of the runs and the retention the unique identities are bounded by.
type aggregatorSettings struct {
	interval          time.Duration
	entitiesRetention time.Duration
}

func (s aggregatorSettings) lockTTL() time.Duration {
	return s.interval + lockMargin
}

type aggregator struct {
	store          aggregatorStore
	settings       aggregatorSettings
	metrics        *aggregatorMetrics
	log            jobLog
	now            func() time.Time
	lockErrorPause time.Duration
}

func newAggregator(s aggregatorStore, settings aggregatorSettings, m *aggregatorMetrics) *aggregator {
	return &aggregator{store: s, settings: settings, metrics: m, log: defaultJobLog(),
		now: func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }, lockErrorPause: lockErrorPause}
}

// run samples the active rooms, participant sessions, egresses and ingress sessions into the peaks of the current buckets, then, within the time
// budget, computes the unique identities of the closed days that lack them.
func (a *aggregator) run(ctx context.Context) error {
	started := a.now()
	err := a.samplePeaks(ctx, started)
	if err == nil && a.now().Before(started.Add(a.settings.interval)) {
		err = a.dailyUniqueIdentities(ctx, started)
	}
	duration := a.now().Sub(started)
	a.metrics.observe(err, duration)
	if err != nil {
		a.log.warnw("analytics aggregator: run failed", err, "durationMs", duration.Milliseconds())
	} else {
		a.log.debugw("analytics aggregator: run", "durationMs", duration.Milliseconds())
	}
	return err
}

// samplePeaks writes the active rooms, STANDARD participant sessions, egresses and ingress sessions at `at` into the
// peaks of the global scope, and the rooms and sessions into those of the media node labels that are active or alive.
func (a *aggregator) samplePeaks(ctx context.Context, at time.Time) error {
	rooms, err := a.store.ActiveRooms(ctx)
	if err != nil {
		return err
	}
	participants, err := a.store.ActiveParticipants(ctx)
	if err != nil {
		return err
	}
	egress, err := a.store.ActiveEgress(ctx)
	if err != nil {
		return err
	}
	ingress, err := a.store.ActiveIngressSessions(ctx)
	if err != nil {
		return err
	}
	labels, err := a.store.LiveMediaNodes(ctx, at)
	if err != nil {
		return err
	}
	for _, byNode := range []map[string]int32{rooms.ByNode, participants.ByNode} {
		for label := range byNode {
			if !slices.Contains(labels, label) {
				labels = append(labels, label)
			}
		}
	}
	slices.Sort(labels)

	increments := projection.PeakIncrements("global", at, projection.Peaks{Rooms: rooms.Total,
		Participants: participants.Total, Egress: egress, Ingress: ingress})
	for _, label := range labels {
		increments = append(increments, projection.PeakIncrements("node:"+label, at,
			projection.Peaks{Rooms: rooms.ByNode[label], Participants: participants.ByNode[label]})...)
	}
	return a.store.ApplyIncrements(ctx, increments)
}

// dailyUniqueIdentities sets the unique identities of the closed UTC days inside the entities retention that lack them.
func (a *aggregator) dailyUniqueIdentities(ctx context.Context, now time.Time) error {
	day := 24 * time.Hour
	from := now.Add(-a.settings.entitiesRetention).Truncate(day).Add(day)
	to := now.Add(-dayCloseGrace).Truncate(day)
	days, err := a.store.DaysWithoutUniqueIdentities(ctx, from, to, uniqueIdentityDaysPerRun)
	if err != nil {
		return err
	}
	for _, start := range days {
		global, perNode, err := a.store.UniqueIdentities(ctx, start, start.Add(day))
		if err != nil {
			return err
		}
		if err := a.store.SetUniqueIdentities(ctx, start, global, perNode); err != nil {
			return err
		}
		a.log.debugw("analytics aggregator: unique identities of a closed day", "day", start, "global", global,
			"nodes", len(perNode))
	}
	return nil
}

// loop runs the aggregator until ctx is cancelled, on one node at a time: once right after obtaining the lock for the
// first time, then every interval while holding it.
func (a *aggregator) loop(ctx context.Context, locks locker) {
	job := &leaderJob{name: "analytics aggregator", key: aggregatorLockKey, interval: a.settings.interval,
		ttl: a.settings.lockTTL(), lockErrorPause: a.lockErrorPause, log: a.log,
		cycle: func(ctx context.Context, _ lease, _ time.Duration) { _ = a.run(ctx) }}
	job.loop(ctx, locks)
}
