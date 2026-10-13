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
	"time"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/openvidu/analytics/projection"
	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/store"
)

// eventEntry is a queued event with the steps of its batch already applied, so that a retry after a transient
// error redoes only the remaining ones.
type eventEntry struct {
	event        *livekit.AnalyticsEvent
	projected    bool
	result       projection.EventResult
	entitiesDone bool
	rawDone      bool
	countable    bool
	rollupsDone  bool
}

// statsEntry is one flush of stats (one Send call), the unit that the participant stats detail merges.
type statsEntry struct {
	stats       []*livekit.AnalyticsStat
	projected   bool
	result      projection.StatsResult
	insertDone  bool
	deltasDone  bool
	rollupsDone bool
}

func (e *eventEntry) done() bool {
	return e.entitiesDone && e.rawDone && e.rollupsDone
}

func (e *statsEntry) done() bool {
	return e.insertDone && e.deltasDone && e.rollupsDone
}

// batchStore is what a batch needs from the database; *store.Store implements it.
type batchStore interface {
	ApplyUpdates(ctx context.Context, updates []projection.Update) error
	ApplyCascades(ctx context.Context, cascades []projection.Update) error
	ApplyIncrements(ctx context.Context, increments []projection.Increment) error
	InsertEvents(ctx context.Context, events []schema.EventDocument) (map[string]bool, error)
	InsertStats(ctx context.Context, stats []schema.TrackStatsDocument) error
	ParticipantSessions(ctx context.Context, roomSids []string) (map[string]int32, error)
	Identities(ctx context.Context, sids []string) (map[string]string, error)
	Tracks(ctx context.Context, trackSids []string, publisherSids []string) (map[string]projection.TrackRef, map[string][]string, error)
	Heartbeat(ctx context.Context, nodeID string, serverVersion string, region string, at time.Time) error
	RoundTrips() int
	ResetRoundTrips()
}

// processor applies batches: entity bulk writes, raw event inserts, rollup increments gated by the raw insert,
// then stats inserts and their deltas.
type processor struct {
	store batchStore
	opts  projection.Options
	cache *refCache
	// onWriteErrors receives rejected writes (validation in error mode, oversized documents); they are dropped.
	onWriteErrors func(err *store.WriteError)
	// onUnsupported receives events of a type the projection does not handle.
	onUnsupported func(event *livekit.AnalyticsEvent)
}

// apply applies one batch. It returns nil when every step succeeded or failed for good, and the transient error
// otherwise; entries keep the steps already applied.
func (p *processor) apply(ctx context.Context, events []*eventEntry, stats []*statsEntry) error {
	if err := p.prefetch(ctx, events); err != nil {
		return err
	}
	for _, entry := range events {
		if !entry.projected {
			entry.result = projection.Event(entry.event, p.cache.refsFor(entry.event), p.opts)
			entry.projected = true
			if !projection.Supported(entry.event.GetType()) && p.onUnsupported != nil {
				p.onUnsupported(entry.event)
			}
		}
		p.cache.learn(entry.event)
	}

	if err := p.applyEntities(ctx, events); err != nil {
		return err
	}
	if err := p.insertRaw(ctx, events); err != nil {
		return err
	}
	if err := p.applyRollups(ctx, events); err != nil {
		return err
	}
	return p.applyStats(ctx, stats)
}

// prefetch loads, with one query per kind, the identities and track references the cache misses: subscribers and
// publishers of track events, tracks of subscription events, and the published tracks of leaving participants.
func (p *processor) prefetch(ctx context.Context, events []*eventEntry) error {
	var missingIdentities, missingTracks, leaving []string
	seen := map[string]bool{}
	for _, entry := range events {
		if entry.projected {
			continue
		}
		event := entry.event
		participantSid := firstNonEmpty(event.GetParticipantId(), event.GetParticipant().GetSid())
		trackSid := firstNonEmpty(event.GetTrackId(), event.GetTrack().GetSid())
		switch event.GetType() {
		case livekit.AnalyticsEventType_PARTICIPANT_JOINED, livekit.AnalyticsEventType_PARTICIPANT_ACTIVE,
			livekit.AnalyticsEventType_PARTICIPANT_RESUMED:
			seen[participantSid] = true
		case livekit.AnalyticsEventType_PARTICIPANT_LEFT, livekit.AnalyticsEventType_PARTICIPANT_CONNECTION_ABORTED:
			leaving = append(leaving, participantSid)
		case livekit.AnalyticsEventType_TRACK_PUBLISHED:
			seen[trackSid] = true
		}
		if isTrackFamily(event.GetType()) {
			if _, ok := p.cache.identities.Get(participantSid); !ok && participantSid != "" && !seen[participantSid] {
				missingIdentities = append(missingIdentities, participantSid)
			}
			if _, ok := p.cache.tracks.Get(trackSid); !ok && trackSid != "" && !seen[trackSid] {
				missingTracks = append(missingTracks, trackSid)
			}
		}
	}
	if len(missingIdentities)+len(missingTracks)+len(leaving) == 0 {
		return nil
	}

	tracks, byPublisher, err := p.store.Tracks(ctx, missingTracks, leaving)
	if err != nil {
		return err
	}
	for sid, ref := range tracks {
		p.cache.tracks.Add(sid, ref)
		if _, ok := p.cache.identities.Get(ref.ParticipantSID); !ok {
			missingIdentities = append(missingIdentities, ref.ParticipantSID)
		}
	}
	for publisher, trackSids := range byPublisher {
		for _, trackSid := range trackSids {
			p.cache.addParticipantTrack(publisher, trackSid)
		}
	}
	identities, err := p.store.Identities(ctx, missingIdentities)
	if err != nil {
		return err
	}
	for sid, identity := range identities {
		p.cache.identities.Add(sid, identity)
	}
	return nil
}

func isTrackFamily(eventType livekit.AnalyticsEventType) bool {
	switch eventType {
	case livekit.AnalyticsEventType_TRACK_PUBLISHED, livekit.AnalyticsEventType_TRACK_PUBLISHED_UPDATE,
		livekit.AnalyticsEventType_TRACK_MUTED, livekit.AnalyticsEventType_TRACK_UNMUTED,
		livekit.AnalyticsEventType_TRACK_UNPUBLISHED, livekit.AnalyticsEventType_TRACK_PUBLISH_STATS,
		livekit.AnalyticsEventType_TRACK_SUBSCRIBE_REQUESTED, livekit.AnalyticsEventType_TRACK_SUBSCRIBED,
		livekit.AnalyticsEventType_TRACK_SUBSCRIBE_FAILED, livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED,
		livekit.AnalyticsEventType_TRACK_SUBSCRIBE_STATS:
		return true
	}
	return false
}

// rejected reports and drops writes the server refused for good; any other error is returned as is.
func (p *processor) rejected(err error) error {
	if err == nil {
		return nil
	}
	var remaining []error
	for _, e := range flatten(err) {
		var writeErr *store.WriteError
		if errors.As(e, &writeErr) {
			if p.onWriteErrors != nil {
				p.onWriteErrors(writeErr)
			}
			continue
		}
		remaining = append(remaining, e)
	}
	return errors.Join(remaining...)
}

func flatten(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}

func (p *processor) applyEntities(ctx context.Context, events []*eventEntry) error {
	var updates, cascades []projection.Update
	var pending []*eventEntry
	for _, entry := range events {
		if entry.entitiesDone {
			continue
		}
		updates = append(updates, entry.result.Updates...)
		cascades = append(cascades, entry.result.Cascades...)
		pending = append(pending, entry)
	}
	if len(pending) == 0 {
		return nil
	}
	if err := p.rejected(p.store.ApplyUpdates(ctx, projection.MergeUpdates(updates, p.opts))); err != nil {
		return err
	}
	if err := p.rejected(p.store.ApplyCascades(ctx, cascades)); err != nil {
		return err
	}
	for _, entry := range pending {
		entry.entitiesDone = true
	}
	return nil
}

func (p *processor) insertRaw(ctx context.Context, events []*eventEntry) error {
	var documents []schema.EventDocument
	var pending []*eventEntry
	for _, entry := range events {
		if entry.rawDone {
			continue
		}
		if entry.result.Raw == nil {
			entry.rawDone = true
			entry.countable = true
			continue
		}
		documents = append(documents, *entry.result.Raw)
		pending = append(pending, entry)
	}
	if len(documents) == 0 {
		return nil
	}
	inserted, err := p.store.InsertEvents(ctx, documents)
	if err = p.rejected(err); err != nil {
		return err
	}
	for _, entry := range pending {
		entry.rawDone = true
		entry.countable = inserted[entry.result.Raw.ID]
	}
	return nil
}

func (p *processor) applyRollups(ctx context.Context, events []*eventEntry) error {
	var increments []projection.Increment
	var pending []*eventEntry
	var endedRooms []string
	for _, entry := range events {
		if entry.rollupsDone {
			continue
		}
		pending = append(pending, entry)
		if !entry.countable {
			continue
		}
		increments = append(increments, entry.result.Increments...)
		if entry.result.EndedRoom != "" {
			endedRooms = append(endedRooms, entry.result.EndedRoom)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if len(endedRooms) > 0 {
		sessions, err := p.store.ParticipantSessions(ctx, endedRooms)
		if err != nil {
			return err
		}
		for _, entry := range pending {
			if !entry.countable || entry.result.EndedRoom == "" {
				continue
			}
			ts := schema.FromTimestamp(entry.event.GetTimestamp())
			increments = append(increments,
				projection.RoomEndedIncrements(entry.event.GetNodeId(), ts, sessions[entry.result.EndedRoom])...)
		}
	}
	if err := p.rejected(p.store.ApplyIncrements(ctx, projection.MergeIncrements(increments))); err != nil {
		return err
	}
	for _, entry := range pending {
		entry.rollupsDone = true
	}
	return nil
}

func (p *processor) applyStats(ctx context.Context, stats []*statsEntry) error {
	var documents []schema.TrackStatsDocument
	var updates []projection.Update
	var increments []projection.Increment
	for _, entry := range stats {
		if !entry.projected {
			entry.result = projection.Stats(entry.stats, p.opts)
			entry.projected = true
		}
		if !entry.insertDone {
			documents = append(documents, entry.result.Documents...)
		}
		if !entry.deltasDone {
			updates = append(updates, entry.result.Updates...)
		}
		if !entry.rollupsDone {
			increments = append(increments, entry.result.Increments...)
		}
	}

	if len(documents) > 0 {
		if err := p.rejected(p.store.InsertStats(ctx, documents)); err != nil {
			return err
		}
	}
	for _, entry := range stats {
		entry.insertDone = true
	}
	if len(updates) > 0 {
		if err := p.rejected(p.store.ApplyUpdates(ctx, projection.MergeUpdates(updates, p.opts))); err != nil {
			return err
		}
	}
	for _, entry := range stats {
		entry.deltasDone = true
	}
	if len(increments) > 0 {
		if err := p.rejected(p.store.ApplyIncrements(ctx, projection.MergeIncrements(increments))); err != nil {
			return err
		}
	}
	for _, entry := range stats {
		entry.rollupsDone = true
	}
	return nil
}
