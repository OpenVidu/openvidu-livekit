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
	"math"
	"sort"
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

// MOS thresholds of the quality bands: below poorScore a sample is poor (lost samples included), below lostScore lost.
const (
	poorScore = 2.06
	lostScore = 1.25
)

// StatsResult is the projection of a batch of stats.
type StatsResult struct {
	Documents []schema.TrackStatsDocument
	// Updates add bytes and quality to participants and rooms; they are not idempotent, so a batch inserts its stats
	// and applies these deltas at most once.
	Updates    []Update
	Increments []Increment
}

// sample is one AnalyticsStat reduced to the stored measures.
type sample struct {
	stat     *livekit.AnalyticsStat
	ts       time.Time
	kind     string
	bytes    int64
	packets  int64
	lost     int64
	nacks    int64
	plis     int64
	firs     int64
	frames   int64
	rttMs    int64
	jitterMs float64
	layers   map[int32]*schema.RtpLayerStats
}

func newSample(stat *livekit.AnalyticsStat) *sample {
	s := &sample{stat: stat, ts: schema.FromTimestamp(stat.GetTimeStamp()), kind: "up", layers: map[int32]*schema.RtpLayerStats{}}
	if stat.GetKind() == livekit.StreamType_DOWNSTREAM {
		s.kind = "down"
	}
	for _, stream := range stat.GetStreams() {
		s.bytes += int64(stream.GetPrimaryBytes() + stream.GetRetransmitBytes() + stream.GetPaddingBytes())
		s.packets += int64(stream.GetPrimaryPackets()) + int64(stream.GetRetransmitPackets()) + int64(stream.GetPaddingPackets())
		s.lost += int64(stream.GetPacketsLost())
		s.nacks += int64(stream.GetNacks())
		s.plis += int64(stream.GetPlis())
		s.firs += int64(stream.GetFirs())
		s.frames += int64(stream.GetFrames())
		s.rttMs = max(s.rttMs, int64(stream.GetRtt()))
		s.jitterMs = math.Max(s.jitterMs, schema.MicrosToMs(stream.GetJitter()))
		for _, layer := range stream.GetVideoLayers() {
			s.addLayer(layer.GetLayer(), int64(layer.GetBytes()), int64(layer.GetPackets()), int64(layer.GetFrames()))
		}
	}
	return s
}

func (s *sample) addLayer(index int32, bytes int64, packets int64, frames int64) {
	layer, ok := s.layers[index]
	if !ok {
		layer = &schema.RtpLayerStats{Layer: index}
		s.layers[index] = layer
	}
	layer.Bytes += bytes
	layer.Packets = clampInt32(int64(layer.Packets) + packets)
	layer.Frames = clampInt32(int64(layer.Frames) + frames)
}

func (s *sample) layerList() []schema.RtpLayerStats {
	layers := make([]schema.RtpLayerStats, 0, len(s.layers))
	for _, layer := range s.layers {
		layers = append(layers, *layer)
	}
	sort.Slice(layers, func(i, j int) bool { return layers[i].Layer < layers[j].Layer })
	return layers
}

// isPseudoTrack reports the signal (TR_SG…) and data (TR_DT…) channels, whose bytes count for the participant only.
func isPseudoTrack(trackID string) bool {
	return strings.HasPrefix(trackID, "TR_SG") || strings.HasPrefix(trackID, "TR_DT")
}

// quality accumulates the quality samples of stored documents.
type quality struct {
	sum      float64
	count    int32
	min      float64
	poor     int32
	lost     int32
	last     float64
	lastAt   time.Time
	hasScore bool
}

func (q *quality) add(score float64, at time.Time) {
	if score <= 0 {
		return
	}
	q.sum += score
	q.count++
	if !q.hasScore || score < q.min {
		q.min = score
	}
	q.hasScore = true
	if score < poorScore {
		q.poor++
	}
	if score < lostScore {
		q.lost++
	}
	if !at.Before(q.lastAt) {
		q.last = score
		q.lastAt = at
	}
}

type entityDelta struct {
	roomSid  string
	nodeID   string
	up, down int64
	quality  quality
	statsAt  time.Time
}

// Stats projects the stats flushed in a batch. In participant detail the media tracks of one participant, direction
// and flush merge into one document; in track detail each track keeps its own. Signal and data channels only add
// bytes to their participant.
func Stats(stats []*livekit.AnalyticsStat, opts Options) StatsResult {
	type key struct {
		room, participant, kind string
		ts                      time.Time
	}
	var order []key
	groups := map[key][]*sample{}
	participants := map[string]*entityDelta{}
	rooms := map[string]*entityDelta{}
	var participantOrder, roomOrder []string

	delta := func(deltas map[string]*entityDelta, order *[]string, id string, roomSid string, nodeID string) *entityDelta {
		d, ok := deltas[id]
		if !ok {
			d = &entityDelta{roomSid: roomSid, nodeID: nodeID}
			deltas[id] = d
			*order = append(*order, id)
		}
		return d
	}

	type bucketKey struct {
		scope  string
		bucket time.Time
	}
	var result StatsResult
	var connectedOrder []bucketKey
	connected := map[bucketKey][]string{}
	for _, stat := range stats {
		if stat.GetParticipantId() == "" || stat.GetRoomId() == "" {
			continue
		}
		s := newSample(stat)
		participant := delta(participants, &participantOrder, stat.GetParticipantId(), stat.GetRoomId(), stat.GetNode())
		if s.kind == "up" {
			participant.up += s.bytes
		} else {
			participant.down += s.bytes
		}
		participant.statsAt = maxTime(participant.statsAt, s.ts)
		for _, scope := range scopes(stat.GetNode()) {
			k := bucketKey{scope, s.ts.Truncate(fiveMinutes)}
			if _, ok := connected[k]; !ok {
				connectedOrder = append(connectedOrder, k)
			}
			connected[k] = append(connected[k], stat.GetParticipantId())
		}
		if isPseudoTrack(stat.GetTrackId()) {
			continue
		}
		k := key{stat.GetRoomId(), stat.GetParticipantId(), s.kind, s.ts}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], s)
	}

	for _, k := range order {
		samples := groups[k]
		var documents []schema.TrackStatsDocument
		if opts.StatsDetail == "track" {
			for _, s := range samples {
				documents = append(documents, s.document(true))
			}
		} else {
			documents = append(documents, mergeSamples(samples).document(false))
		}

		participant := participants[k.participant]
		room := delta(rooms, &roomOrder, k.room, k.room, samples[0].stat.GetNode())
		room.statsAt = maxTime(room.statsAt, k.ts)
		for i := range documents {
			document := documents[i]
			participant.quality.add(document.Score, document.TS)
			room.quality.add(document.Score, document.TS)
			if document.Meta.Kind == "up" {
				room.up += document.Bytes
			} else {
				room.down += document.Bytes
			}
			result.Increments = append(result.Increments, statsIncrements(document)...)
		}
		result.Documents = append(result.Documents, documents...)
	}

	for _, k := range connectedOrder {
		result.Increments = append(result.Increments, Increment{
			Collection: schema.CollectionAnalytics5Min, Scope: k.scope, Bucket: k.bucket,
			Members: map[string][]string{"internal.connectedSids": connected[k]},
			Sizes:   map[string]string{"participants.connected": "internal.connectedSids"},
		})
	}

	for _, sid := range participantOrder {
		d := participants[sid]
		f := d.fields()
		if d.quality.hasScore {
			f.set("quality.lastScore", latest("quality.lastScore", "quality.lastScoreAt", d.quality.lastAt, d.quality.last))
			f.set("quality.lastScoreAt", maxDate("quality.lastScoreAt", d.quality.lastAt))
		}
		result.Updates = append(result.Updates, Update{Collection: schema.CollectionParticipants, ID: sid,
			Pipeline: stagesOf(f)})
	}
	for _, sid := range roomOrder {
		result.Updates = append(result.Updates, Update{Collection: schema.CollectionRooms, ID: sid,
			Pipeline: stagesOf(rooms[sid].fields())})
	}
	return result
}

func stagesOf(f *fields) []bson.D {
	return []bson.D{stage(f.doc())}
}

func maxTime(a time.Time, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// fields adds a delta to bytes and quality and advances lastStatsAt.
func (d *entityDelta) fields() *fields {
	f := newFields()
	f.set("bytes.up", add(ifNull("bytes.up", int64(0)), d.up))
	f.set("bytes.down", add(ifNull("bytes.down", int64(0)), d.down))
	f.set("quality.scoreSum", add(ifNull("quality.scoreSum", float64(0)), d.quality.sum))
	f.set("quality.scoreCount", add(ifNull("quality.scoreCount", int32(0)), d.quality.count))
	f.set("quality.poor", add(ifNull("quality.poor", int32(0)), d.quality.poor))
	f.set("quality.lost", add(ifNull("quality.lost", int32(0)), d.quality.lost))
	if d.quality.hasScore {
		f.set("quality.minScore", minNumber("quality.minScore", d.quality.min))
	}
	if !d.statsAt.IsZero() {
		f.set("lastStatsAt", maxDate("lastStatsAt", d.statsAt))
	}
	return f
}

func statsIncrements(document schema.TrackStatsDocument) []Increment {
	counters := map[string]any{"bytes." + document.Meta.Kind: document.Bytes}
	if document.Score > 0 {
		counters["quality.scoreSum"] = document.Score
		counters["quality.scoreCount"] = one
		if document.Score < poorScore {
			counters["quality.poor"] = one
		}
		if document.Score < lostScore {
			counters["quality.lost"] = one
		}
	}
	increments := periodIncrements(document.Meta.NodeID, document.TS, counters)
	for _, scope := range scopes(document.Meta.NodeID) {
		increments = append(increments, Increment{Collection: schema.CollectionAnalytics5Min, Scope: scope,
			Bucket: document.TS.Truncate(fiveMinutes), Counters: counters})
	}
	return increments
}

// document turns a sample into a track_stats document; withTrack keeps the track SID and MIME type.
func (s *sample) document(withTrack bool) schema.TrackStatsDocument {
	document := schema.TrackStatsDocument{
		TS: s.ts,
		Meta: schema.TrackStatsMeta{
			RoomSID:        s.stat.GetRoomId(),
			ParticipantSID: s.stat.GetParticipantId(),
			Kind:           s.kind,
			NodeID:         s.stat.GetNode(),
		},
		Score:       roundScore(float64(s.stat.GetScore())),
		MinScore:    roundScore(float64(s.stat.GetMinScore())),
		MedianScore: roundScore(float64(s.stat.GetMedianScore())),
		Bytes:       s.bytes,
		Packets:     clampInt32(s.packets),
		PacketsLost: clampInt32(s.lost),
		RttMs:       clampInt32(s.rttMs),
		JitterMs:    s.jitterMs,
		Nacks:       clampInt32(s.nacks),
		Plis:        clampInt32(s.plis),
		Firs:        clampInt32(s.firs),
		Frames:      clampInt32(s.frames),
		Layers:      s.layerList(),
	}
	if withTrack {
		document.Meta.TrackSID = s.stat.GetTrackId()
		document.MimeType = s.stat.GetMime()
	}
	return document
}

// roundScore keeps two decimals of a MOS reported as float32.
func roundScore(score float64) float64 {
	return math.Round(score*100) / 100
}

// mergeSamples merges the tracks of one participant, direction and flush: counters and layers are summed, RTT and
// jitter take their maximum, the score is the mean of the non-zero scores, the minimum score the lowest non-zero
// one and the median score the median of the track medians.
func mergeSamples(samples []*sample) *sample {
	merged := &sample{stat: &livekit.AnalyticsStat{
		RoomId: samples[0].stat.GetRoomId(), ParticipantId: samples[0].stat.GetParticipantId(),
		Node: samples[0].stat.GetNode(), Kind: samples[0].stat.GetKind(),
	}, ts: samples[0].ts, kind: samples[0].kind, layers: map[int32]*schema.RtpLayerStats{}}

	var scores, minimums, medians []float64
	for _, s := range samples {
		merged.bytes += s.bytes
		merged.packets += s.packets
		merged.lost += s.lost
		merged.nacks += s.nacks
		merged.plis += s.plis
		merged.firs += s.firs
		merged.frames += s.frames
		merged.rttMs = max(merged.rttMs, s.rttMs)
		merged.jitterMs = math.Max(merged.jitterMs, s.jitterMs)
		for _, layer := range s.layers {
			merged.addLayer(layer.Layer, layer.Bytes, int64(layer.Packets), int64(layer.Frames))
		}
		if score := float64(s.stat.GetScore()); score > 0 {
			scores = append(scores, score)
		}
		if score := float64(s.stat.GetMinScore()); score > 0 {
			minimums = append(minimums, score)
		}
		if score := float64(s.stat.GetMedianScore()); score > 0 {
			medians = append(medians, score)
		}
	}
	merged.stat.Score = float32(mean(scores))
	merged.stat.MinScore = float32(minimum(minimums))
	merged.stat.MedianScore = float32(median(medians))
	return merged
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func minimum(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	lowest := values[0]
	for _, value := range values[1:] {
		lowest = math.Min(lowest, value)
	}
	return lowest
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}
