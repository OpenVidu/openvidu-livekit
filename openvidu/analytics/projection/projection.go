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

// Package projection turns analytics events and stats into write models of the v2 data model. Projections are pure
// functions of the event, the references the batch resolved and the options: they never read the database.
//
// Entity documents are updated with aggregation pipelines, one $set stage per event, so the updates of one document
// in a batch merge by concatenation in event order, statuses advance by rank and never regress, and counters derive
// from id sets (internal.*) in a final stage, which makes replays no-ops. Rollups are $inc counters, summed when
// merged and applied only for events whose raw insert succeeded.
package projection

import (
	"sort"
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// Options holds the configuration that shapes projected documents.
type Options struct {
	Retention   schema.Retention
	StatsDetail string
	Encode      schema.EncodeOptions
	// Stores reports whether events of a type are stored raw.
	Stores func(eventType string) bool
}

// Refs are facts the batch resolved from its cache or with one lookup per batch, because track and subscription
// events carry only participant SIDs.
type Refs struct {
	// Identities maps participant SIDs to identities.
	Identities map[string]string
	// Tracks maps track SIDs to their publisher and source.
	Tracks map[string]TrackRef
	// ParticipantTracks maps participant SIDs to the SIDs of the tracks they published.
	ParticipantTracks map[string][]string
	// IngressStatus maps ingress IDs to the last state status seen before this event, empty when unknown.
	IngressStatus map[string]string
	// Reconciled holds the entities the reconciler closed before their close event arrived: their closure is
	// already counted in the rollups.
	Reconciled map[string]bool
}

// TrackRef describes a published track.
type TrackRef struct {
	ParticipantSID string
	Source         string
	Type           string
}

// Update modifies documents with an aggregation pipeline: the document with _id ID, inserted when missing if Upsert is
// set, or every document matching Filter when ID is empty.
type Update struct {
	Collection string
	ID         string
	Filter     bson.D
	Pipeline   []bson.D
	Upsert     bool
}

// Increment adds counters to a rollup document and members to its sets. Counters hold int32, int64 or float64 values;
// Sizes maps a counter path to the set whose size it reports.
type Increment struct {
	Collection string
	Scope      string
	Bucket     time.Time
	Counters   map[string]any
	Members    map[string][]string
	Sizes      map[string]string
}

// ID returns the rollup document identifier: scope and bucket start in RFC 3339 UTC.
func (i Increment) ID() string {
	return i.Scope + ":" + i.Bucket.UTC().Format(time.RFC3339)
}

// EventResult is the projection of one event.
type EventResult struct {
	Updates []Update
	// Cascades update documents by filter and run after the Updates of the batch.
	Cascades   []Update
	Raw        *schema.EventDocument
	Increments []Increment
	// EndedRoom is the SID of a room closed by the event; its participantsSum increment needs the room counters.
	EndedRoom string
}

// DependentKinds are participant kinds that depend on others and do not keep a room open.
var DependentKinds = []string{livekit.ParticipantInfo_EGRESS.String(), livekit.ParticipantInfo_AGENT.String()}

// StandardKind is the only participant kind the participant counters count; byKind counts every kind.
var StandardKind = livekit.ParticipantInfo_STANDARD.String()

// Supported reports the event types the projection handles; any other type yields an empty result.
func Supported(eventType livekit.AnalyticsEventType) bool {
	switch eventType {
	case livekit.AnalyticsEventType_ROOM_CREATED, livekit.AnalyticsEventType_ROOM_ENDED,
		livekit.AnalyticsEventType_PARTICIPANT_JOINED, livekit.AnalyticsEventType_PARTICIPANT_ACTIVE,
		livekit.AnalyticsEventType_PARTICIPANT_RESUMED, livekit.AnalyticsEventType_PARTICIPANT_LEFT,
		livekit.AnalyticsEventType_PARTICIPANT_CONNECTION_ABORTED,
		livekit.AnalyticsEventType_TRACK_PUBLISH_REQUESTED, livekit.AnalyticsEventType_TRACK_PUBLISHED,
		livekit.AnalyticsEventType_TRACK_PUBLISHED_UPDATE, livekit.AnalyticsEventType_TRACK_MUTED,
		livekit.AnalyticsEventType_TRACK_UNMUTED, livekit.AnalyticsEventType_TRACK_UNPUBLISHED,
		livekit.AnalyticsEventType_TRACK_PUBLISH_STATS, livekit.AnalyticsEventType_TRACK_MAX_SUBSCRIBED_VIDEO_QUALITY,
		livekit.AnalyticsEventType_TRACK_SUBSCRIBE_REQUESTED, livekit.AnalyticsEventType_TRACK_SUBSCRIBED,
		livekit.AnalyticsEventType_TRACK_SUBSCRIBE_FAILED, livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED,
		livekit.AnalyticsEventType_TRACK_SUBSCRIBE_STATS,
		livekit.AnalyticsEventType_EGRESS_STARTED, livekit.AnalyticsEventType_EGRESS_UPDATED,
		livekit.AnalyticsEventType_EGRESS_ENDED,
		livekit.AnalyticsEventType_INGRESS_CREATED, livekit.AnalyticsEventType_INGRESS_STARTED,
		livekit.AnalyticsEventType_INGRESS_UPDATED, livekit.AnalyticsEventType_INGRESS_ENDED,
		livekit.AnalyticsEventType_INGRESS_DELETED, livekit.AnalyticsEventType_API_CALL:
		return true
	}
	return false
}

// Ignored reports the event types the server emits that the data model leaves out on purpose: the delivery record of
// each webhook.
func Ignored(eventType livekit.AnalyticsEventType) bool {
	return eventType == livekit.AnalyticsEventType_WEBHOOK
}

// Event projects one analytics event. Unknown types yield an empty result.
func Event(event *livekit.AnalyticsEvent, refs Refs, opts Options) EventResult {
	ts := schema.FromTimestamp(event.GetTimestamp())
	if ts.IsZero() {
		ts = time.Now().UTC().Truncate(time.Millisecond)
	}
	p := projector{event: event, refs: refs, opts: opts, ts: ts}

	switch event.GetType() {
	case livekit.AnalyticsEventType_ROOM_CREATED, livekit.AnalyticsEventType_ROOM_ENDED:
		p.room()
	case livekit.AnalyticsEventType_PARTICIPANT_JOINED, livekit.AnalyticsEventType_PARTICIPANT_ACTIVE,
		livekit.AnalyticsEventType_PARTICIPANT_RESUMED, livekit.AnalyticsEventType_PARTICIPANT_LEFT,
		livekit.AnalyticsEventType_PARTICIPANT_CONNECTION_ABORTED:
		p.participant()
	case livekit.AnalyticsEventType_TRACK_PUBLISH_REQUESTED, livekit.AnalyticsEventType_TRACK_PUBLISHED,
		livekit.AnalyticsEventType_TRACK_PUBLISHED_UPDATE, livekit.AnalyticsEventType_TRACK_MUTED,
		livekit.AnalyticsEventType_TRACK_UNMUTED, livekit.AnalyticsEventType_TRACK_UNPUBLISHED,
		livekit.AnalyticsEventType_TRACK_PUBLISH_STATS, livekit.AnalyticsEventType_TRACK_MAX_SUBSCRIBED_VIDEO_QUALITY:
		p.track()
	case livekit.AnalyticsEventType_TRACK_SUBSCRIBE_REQUESTED, livekit.AnalyticsEventType_TRACK_SUBSCRIBED,
		livekit.AnalyticsEventType_TRACK_SUBSCRIBE_FAILED, livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED,
		livekit.AnalyticsEventType_TRACK_SUBSCRIBE_STATS:
		p.subscription()
	case livekit.AnalyticsEventType_EGRESS_STARTED, livekit.AnalyticsEventType_EGRESS_UPDATED,
		livekit.AnalyticsEventType_EGRESS_ENDED:
		p.egress()
	case livekit.AnalyticsEventType_INGRESS_CREATED, livekit.AnalyticsEventType_INGRESS_STARTED,
		livekit.AnalyticsEventType_INGRESS_UPDATED, livekit.AnalyticsEventType_INGRESS_ENDED,
		livekit.AnalyticsEventType_INGRESS_DELETED:
		p.ingress()
	case livekit.AnalyticsEventType_API_CALL:
		p.apiCall()
		return p.result
	default:
		return EventResult{}
	}

	p.rollups()
	p.raw()
	return p.result
}

// projector holds the event being projected and accumulates its write models.
type projector struct {
	event  *livekit.AnalyticsEvent
	refs   Refs
	opts   Options
	ts     time.Time
	result EventResult
}

// upsert adds an update that creates the document when it does not exist yet.
func (p *projector) upsert(collection string, id string, f *fields) {
	p.result.Updates = append(p.result.Updates,
		Update{Collection: collection, ID: id, Pipeline: []bson.D{stage(f.doc())}, Upsert: true})
}

// amend adds an update that applies only to an existing document, for secondary effects of an event (a counter of
// another entity) that must not create a partial document.
func (p *projector) amend(collection string, id string, f *fields) {
	p.result.Updates = append(p.result.Updates, Update{Collection: collection, ID: id, Pipeline: []bson.D{stage(f.doc())}})
}

func (p *projector) retentionMs(class schema.RetentionClass) int64 {
	retention, _, _ := p.opts.Retention.Of(class)
	return retention.Milliseconds()
}

// roomRef returns the SID and name of the room an event belongs to: room_id or room.sid, the room of an egress or
// the current room of an ingress.
func roomRef(event *livekit.AnalyticsEvent) (sid string, name string) {
	sid = firstNonEmpty(event.GetRoomId(), event.GetRoom().GetSid(), event.GetEgress().GetRoomId(),
		event.GetIngress().GetState().GetRoomId())
	name = firstNonEmpty(event.GetRoom().GetName(), event.GetEgress().GetRoomName(), event.GetIngress().GetRoomName())
	return sid, name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// enumName strips a prefix from a protocol enum name: ROOM_END_API_DELETE becomes API_DELETE.
func enumName(name string, prefix string) string {
	return strings.TrimPrefix(name, prefix)
}

// MergeUpdates merges the updates of the same document in event order into one update and appends the stage that
// derives the counters of each collection. Cascades are returned unchanged after them.
func MergeUpdates(updates []Update, opts Options) []Update {
	type key struct{ collection, id string }
	var order []key
	merged := map[key]*Update{}
	for _, update := range updates {
		k := key{update.Collection, update.ID}
		if existing, ok := merged[k]; ok {
			existing.Pipeline = append(existing.Pipeline, update.Pipeline...)
			existing.Upsert = existing.Upsert || update.Upsert
			continue
		}
		copied := update
		copied.Pipeline = append([]bson.D(nil), update.Pipeline...)
		merged[k] = &copied
		if update.Upsert {
			copied.Upsert = true
		}
		order = append(order, k)
	}

	result := make([]Update, 0, len(order))
	for _, k := range order {
		update := merged[k]
		if derive := deriveStage(update.Collection, opts); derive != nil {
			update.Pipeline = append(update.Pipeline, derive)
		}
		result = append(result, *update)
	}
	return result
}

// MergeIncrements sums the increments of the same rollup document.
func MergeIncrements(increments []Increment) []Increment {
	type key struct{ collection, id string }
	var order []key
	merged := map[key]*Increment{}
	for _, increment := range increments {
		k := key{increment.Collection, increment.ID()}
		existing, ok := merged[k]
		if !ok {
			existing = &Increment{Collection: increment.Collection, Scope: increment.Scope, Bucket: increment.Bucket,
				Counters: map[string]any{}, Members: map[string][]string{}, Sizes: map[string]string{}}
			merged[k] = existing
			order = append(order, k)
		}
		for path, value := range increment.Counters {
			existing.Counters[path] = sum(existing.Counters[path], value)
		}
		for path, members := range increment.Members {
			existing.Members[path] = append(existing.Members[path], members...)
		}
		for path, source := range increment.Sizes {
			existing.Sizes[path] = source
		}
	}

	result := make([]Increment, 0, len(order))
	for _, k := range order {
		increment := merged[k]
		for path, members := range increment.Members {
			sort.Strings(members)
			increment.Members[path] = dedupe(members)
		}
		result = append(result, *increment)
	}
	return result
}

func sum(a any, b any) any {
	switch v := b.(type) {
	case int32:
		if current, ok := a.(int32); ok {
			return current + v
		}
	case int64:
		if current, ok := a.(int64); ok {
			return current + v
		}
	case float64:
		if current, ok := a.(float64); ok {
			return current + v
		}
	}
	return b
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, value := range sorted {
		if i == 0 || value != sorted[i-1] {
			out = append(out, value)
		}
	}
	return out
}

// RenderIncrement returns the update document or pipeline that applies an increment to its rollup document.
func RenderIncrement(increment Increment) any {
	if len(increment.Members) == 0 {
		inc := bson.D{}
		for _, path := range sortedKeys(increment.Counters) {
			inc = append(inc, bson.E{Key: path, Value: increment.Counters[path]})
		}
		return bson.D{
			{Key: "$setOnInsert", Value: bson.D{{Key: "scope", Value: increment.Scope}, {Key: "bucket", Value: increment.Bucket}}},
			{Key: "$inc", Value: inc},
		}
	}

	fields := bson.D{
		{Key: "scope", Value: keep("scope", increment.Scope)},
		{Key: "bucket", Value: keep("bucket", increment.Bucket)},
	}
	for _, path := range sortedKeys(increment.Counters) {
		value := increment.Counters[path]
		fields = append(fields, bson.E{Key: path, Value: add(ifNull(path, zeroOf(value)), value)})
	}
	for _, path := range sortedKeys(increment.Members) {
		members := make(bson.A, len(increment.Members[path]))
		for i, member := range increment.Members[path] {
			members[i] = member
		}
		fields = append(fields, bson.E{Key: path, Value: bson.D{{Key: "$setUnion", Value: bson.A{emptyArray(path), members}}}})
	}
	sizes := bson.D{}
	for _, path := range sortedKeys(increment.Sizes) {
		sizes = append(sizes, bson.E{Key: path, Value: sizeOf(increment.Sizes[path])})
	}
	pipeline := []bson.D{stage(fields)}
	if len(sizes) > 0 {
		pipeline = append(pipeline, stage(sizes))
	}
	return pipeline
}

func zeroOf(value any) any {
	switch value.(type) {
	case int64:
		return int64(0)
	case float64:
		return float64(0)
	default:
		return int32(0)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
