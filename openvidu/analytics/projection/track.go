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
	"strconv"
	"time"

	"github.com/livekit/protocol/livekit"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
)

const (
	trackPublished   = "published"
	trackUnpublished = "unpublished"

	subscriptionRequested    = "requested"
	subscriptionFailed       = "failed"
	subscriptionSubscribed   = "subscribed"
	subscriptionUnsubscribed = "unsubscribed"
)

var trackRanks = []rank{{trackPublished, 0}, {trackUnpublished, 1}}

// A failed first attempt can still succeed after a retry, so failed ranks below subscribed.
var subscriptionRanks = []rank{
	{subscriptionRequested, 0}, {subscriptionFailed, 1}, {subscriptionSubscribed, 2}, {subscriptionUnsubscribed, 3},
}

// fullTrackInfo reports whether a TrackInfo describes the track (TRACK_PUBLISHED, TRACK_SUBSCRIBED) rather than
// carrying only its SID (TRACK_SUBSCRIBE_FAILED, TRACK_UNSUBSCRIBED).
func fullTrackInfo(info *livekit.TrackInfo) bool {
	return info.GetSource() != livekit.TrackSource_UNKNOWN || info.GetMimeType() != "" || info.GetName() != ""
}

// trackFacts sets the media facts of a track; with onlyMissing the facts already stored are kept.
func trackFacts(f *fields, info *livekit.TrackInfo, onlyMissing bool) {
	if !fullTrackInfo(info) {
		return
	}
	assign := func(path string, value any) {
		if onlyMissing {
			f.set(path, keep(path, value))
		} else {
			f.set(path, value)
		}
	}
	assign("type", info.GetType().String())
	assign("source", info.GetSource().String())
	if info.GetMimeType() != "" {
		assign("mimeType", info.GetMimeType())
	}
	if info.GetName() != "" {
		assign("name", info.GetName())
	}
	if info.GetWidth() > 0 {
		assign("width", int32(info.GetWidth()))
	}
	if info.GetHeight() > 0 {
		assign("height", int32(info.GetHeight()))
	}
	if info.GetType() == livekit.TrackType_VIDEO {
		assign("simulcast", info.GetSimulcast())
	}
	if codecs := codecDocuments(info); len(codecs) > 0 {
		assign("codecs", codecs)
	}
	assign("encryption", info.GetEncryption().String())
}

func codecDocuments(info *livekit.TrackInfo) bson.A {
	codecs := bson.A{}
	for _, codec := range info.GetCodecs() {
		if codec.GetMimeType() == "" {
			continue
		}
		document := bson.D{{Key: "mimeType", Value: codec.GetMimeType()}}
		if layers := layerDocuments(codec.GetLayers()); len(layers) > 0 {
			document = append(document, bson.E{Key: "layers", Value: layers})
		}
		codecs = append(codecs, document)
	}
	if len(codecs) == 0 && info.GetMimeType() != "" {
		document := bson.D{{Key: "mimeType", Value: info.GetMimeType()}}
		if layers := layerDocuments(info.GetLayers()); len(layers) > 0 {
			document = append(document, bson.E{Key: "layers", Value: layers})
		}
		codecs = append(codecs, document)
	}
	return codecs
}

func layerDocuments(layers []*livekit.VideoLayer) bson.A {
	documents := bson.A{}
	for _, layer := range layers {
		document := bson.D{{Key: "quality", Value: layer.GetQuality().String()}}
		if layer.GetWidth() > 0 {
			document = append(document, bson.E{Key: "width", Value: int32(layer.GetWidth())})
		}
		if layer.GetHeight() > 0 {
			document = append(document, bson.E{Key: "height", Value: int32(layer.GetHeight())})
		}
		if layer.GetBitrate() > 0 {
			document = append(document, bson.E{Key: "bitrate", Value: int32(min(layer.GetBitrate(), math.MaxInt32))})
		}
		documents = append(documents, document)
	}
	return documents
}

// trackBase returns the fields every write that may create a track document sets.
func (p *projector) trackBase(trackSid string, roomSid string, publisherSid string, publisherIdentity string) *fields {
	f := newFields()
	if roomSid != "" {
		f.set("roomSid", keep("roomSid", roomSid))
	}
	if publisherSid != "" {
		f.set("participantSid", keep("participantSid", publisherSid))
	}
	if publisherIdentity != "" {
		f.set("identity", keep("identity", publisherIdentity))
	}
	f.set("status", keep("status", trackPublished))
	f.set("publishedAt", minDate("publishedAt", p.ts))
	f.set("muted", keep("muted", false))
	f.set("schemaVersion", int32(schema.SchemaVersion))
	if ref, ok := p.refs.Tracks[trackSid]; ok {
		f.set("type", keep("type", ref.Type))
		f.set("source", keep("source", ref.Source))
	}
	return f
}

func (p *projector) track() {
	info := p.event.GetTrack()
	trackSid := firstNonEmpty(p.event.GetTrackId(), info.GetSid())
	roomSid, roomName := roomRef(p.event)
	publisherSid := firstNonEmpty(p.event.GetParticipantId(), p.event.GetParticipant().GetSid())
	if trackSid == "" {
		return
	}
	publisherIdentity := firstNonEmpty(p.event.GetParticipant().GetIdentity(), p.refs.Identities[publisherSid])

	switch p.event.GetType() {
	case livekit.AnalyticsEventType_TRACK_PUBLISHED, livekit.AnalyticsEventType_TRACK_PUBLISHED_UPDATE:
		f := p.trackBase(trackSid, roomSid, publisherSid, publisherIdentity)
		trackFacts(f, info, false)
		if p.event.GetType() == livekit.AnalyticsEventType_TRACK_PUBLISHED {
			f.set("status", ranked("status", trackPublished, trackRanks))
			f.set("muted", info.GetMuted())
			p.publishedBy(publisherSid, roomSid, roomName, trackSid, info.GetSource().String())
		}
		p.upsert(schema.CollectionTracks, trackSid, f)
	case livekit.AnalyticsEventType_TRACK_MUTED, livekit.AnalyticsEventType_TRACK_UNMUTED:
		f := p.trackBase(trackSid, roomSid, publisherSid, publisherIdentity)
		trackFacts(f, info, true)
		muted := p.event.GetType() == livekit.AnalyticsEventType_TRACK_MUTED
		f.set("muted", latest("muted", "internal.mutedAt", p.ts, muted))
		f.set("internal.mutedAt", maxDate("internal.mutedAt", p.ts))
		p.upsert(schema.CollectionTracks, trackSid, f)
		p.touchParticipant(publisherSid)
	case livekit.AnalyticsEventType_TRACK_UNPUBLISHED:
		f := p.trackBase(trackSid, roomSid, publisherSid, publisherIdentity)
		trackFacts(f, info, true)
		closedByEvent := eq(field("closedBy"), byEvent)
		f.set("status", trackUnpublished)
		f.set("unpublishedAt", cond(closedByEvent, field("unpublishedAt"), p.ts))
		f.set("closedBy", byEvent)
		p.upsert(schema.CollectionTracks, trackSid, f)
		p.touchParticipant(publisherSid)
	case livekit.AnalyticsEventType_TRACK_PUBLISH_STATS:
		layer := strconv.Itoa(int(p.event.GetVideoLayer()))
		f := newFields()
		f.set("internal.publishStats."+layer, finalStats(p.event.GetRtpStats(), p.event.GetVideoLayer()))
		p.amend(schema.CollectionTracks, trackSid, f)
	}
}

// touchParticipant advances the lastEventAt of the participant session an event concerns.
func (p *projector) touchParticipant(sid string) {
	if sid == "" {
		return
	}
	f := newFields()
	f.set("lastEventAt", maxDate("lastEventAt", p.ts))
	p.amend(schema.CollectionParticipants, sid, f)
}

// publishedBy records a published track on its publisher and its room.
func (p *projector) publishedBy(publisherSid string, roomSid string, roomName string, trackSid string, source string) {
	if publisherSid != "" {
		participant := newFields()
		participant.set("internal.trackSids", union("internal.trackSids", trackSid))
		participant.set("internal.tracksBySource."+source, union("internal.tracksBySource."+source, trackSid))
		participant.set("isPublisher", true)
		participant.set("lastEventAt", maxDate("lastEventAt", p.ts))
		p.amend(schema.CollectionParticipants, publisherSid, participant)
	}
	if roomSid != "" {
		room := p.roomTouch(roomName)
		room.set("internal.trackSids", union("internal.trackSids", trackSid))
		room.set("internal.tracksBySource."+source, union("internal.tracksBySource."+source, trackSid))
		p.upsert(schema.CollectionRooms, roomSid, room)
	}
}

// finalStats converts the final RTPStats of a publication layer or a subscription; jitter is reported in
// microseconds and duration in seconds.
func finalStats(stats *livekit.RTPStats, layer int32) bson.D {
	packets := int64(stats.GetPackets())
	lost := int64(stats.GetPacketsLost())
	lossPct := float64(0)
	if packets+lost > 0 {
		lossPct = math.Round(float64(lost)/float64(packets+lost)*10000) / 100
	}
	return bson.D{
		{Key: "layer", Value: layer},
		{Key: "bytes", Value: int64(stats.GetBytes())},
		{Key: "packets", Value: clampInt32(packets)},
		{Key: "packetsLost", Value: clampInt32(lost)},
		{Key: "packetLossPct", Value: lossPct},
		{Key: "nacks", Value: clampInt32(int64(stats.GetNacks()))},
		{Key: "plis", Value: clampInt32(int64(stats.GetPlis()))},
		{Key: "firs", Value: clampInt32(int64(stats.GetFirs()))},
		{Key: "frames", Value: clampInt32(int64(stats.GetFrames()))},
		{Key: "rttMaxMs", Value: clampInt32(int64(stats.GetRttMax()))},
		{Key: "jitterMaxMs", Value: math.Round(stats.GetJitterMax()) / 1000},
		{Key: "durationMs", Value: int64(math.Round(stats.GetDuration() * 1000))},
	}
}

func clampInt32(value int64) int32 {
	return int32(min(value, math.MaxInt32))
}

func (p *projector) subscription() {
	info := p.event.GetTrack()
	trackSid := firstNonEmpty(p.event.GetTrackId(), info.GetSid())
	subscriberSid := firstNonEmpty(p.event.GetParticipantId(), p.event.GetParticipant().GetSid())
	roomSid, roomName := roomRef(p.event)
	if trackSid == "" || subscriberSid == "" {
		return
	}
	id := trackSid + ":" + subscriberSid
	tracksMs := p.retentionMs(schema.RetentionTracks)

	f := newFields()
	f.set("trackSid", trackSid)
	if roomSid != "" {
		f.set("roomSid", keep("roomSid", roomSid))
	}
	f.set("subscriberSid", subscriberSid)
	if identity := p.refs.Identities[subscriberSid]; identity != "" {
		f.set("subscriberIdentity", keep("subscriberIdentity", identity))
	}
	if ref, ok := p.refs.Tracks[trackSid]; ok {
		f.set("source", keep("source", ref.Source))
		if ref.ParticipantSID != "" {
			f.set("publisherSid", keep("publisherSid", ref.ParticipantSID))
			if identity := p.refs.Identities[ref.ParticipantSID]; identity != "" {
				f.set("publisherIdentity", keep("publisherIdentity", identity))
			}
		}
	}
	f.set("status", keep("status", subscriptionRequested))
	f.set("schemaVersion", int32(schema.SchemaVersion))

	switch p.event.GetType() {
	case livekit.AnalyticsEventType_TRACK_SUBSCRIBE_REQUESTED:
		f.set("status", ranked("status", subscriptionRequested, subscriptionRanks))
		p.upsert(schema.CollectionTrackSubscriptions, id, f)
	case livekit.AnalyticsEventType_TRACK_SUBSCRIBED:
		publisher := p.event.GetPublisher()
		f.set("status", ranked("status", subscriptionSubscribed, subscriptionRanks))
		f.set("subscribedAt", minDate("subscribedAt", p.ts))
		if publisher.GetSid() != "" {
			f.set("publisherSid", publisher.GetSid())
		}
		if publisher.GetIdentity() != "" {
			f.set("publisherIdentity", publisher.GetIdentity())
		}
		if fullTrackInfo(info) {
			f.set("source", info.GetSource().String())
		}
		p.upsert(schema.CollectionTrackSubscriptions, id, f)

		subscriber := newFields()
		subscriber.set("internal.subscribedTrackSids", union("internal.subscribedTrackSids", trackSid))
		subscriber.set("lastEventAt", maxDate("lastEventAt", p.ts))
		p.amend(schema.CollectionParticipants, subscriberSid, subscriber)

		track := p.trackBase(trackSid, roomSid, publisher.GetSid(), publisher.GetIdentity())
		trackFacts(track, info, true)
		track.set("internal.subscriberSids", union("internal.subscriberSids", subscriberSid))
		p.upsert(schema.CollectionTracks, trackSid, track)
		if roomSid != "" {
			p.upsert(schema.CollectionRooms, roomSid, p.roomTouch(roomName))
		}
	case livekit.AnalyticsEventType_TRACK_SUBSCRIBE_FAILED:
		f.set("status", ranked("status", subscriptionFailed, subscriptionRanks))
		f.set("error", p.event.GetError())
		f.set("expiresAt", p.ts.Add(time.Duration(tracksMs)*time.Millisecond))
		p.upsert(schema.CollectionTrackSubscriptions, id, f)

		subscriber := newFields()
		subscriber.set("internal.failedTrackSids", union("internal.failedTrackSids", trackSid))
		subscriber.set("lastEventAt", maxDate("lastEventAt", p.ts))
		p.amend(schema.CollectionParticipants, subscriberSid, subscriber)

		track := newFields()
		track.set("internal.failedSubscriberSids", union("internal.failedSubscriberSids", subscriberSid))
		p.amend(schema.CollectionTracks, trackSid, track)
	case livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED:
		closedByEvent := eq(field("closedBy"), byEvent)
		f.set("status", subscriptionUnsubscribed)
		f.set("unsubscribedAt", cond(closedByEvent, field("unsubscribedAt"), p.ts))
		f.set("closedBy", byEvent)
		p.upsert(schema.CollectionTrackSubscriptions, id, f)

		track := newFields()
		track.set("internal.unsubscribedSids", union("internal.unsubscribedSids", subscriberSid))
		p.amend(schema.CollectionTracks, trackSid, track)
		p.touchParticipant(subscriberSid)
	case livekit.AnalyticsEventType_TRACK_SUBSCRIBE_STATS:
		stats := newFields()
		stats.set("subscribeStats", subscribeStats(p.event.GetRtpStats()))
		p.amend(schema.CollectionTrackSubscriptions, id, stats)
	}
}

func subscribeStats(stats *livekit.RTPStats) bson.D {
	document := finalStats(stats, 0)
	out := bson.D{}
	for _, element := range document {
		if element.Key != "layer" && element.Key != "frames" {
			out = append(out, element)
		}
	}
	return out
}

func deriveTrack(opts Options) *fields {
	retention, _, _ := opts.Retention.Of(schema.RetentionTracks)
	layers := bson.D{{Key: "$map", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$objectToArray", Value: ifNull("internal.publishStats", bson.D{})}}},
		{Key: "in", Value: "$$this.v"},
	}}}
	statsOf := func(key string, operator string) bson.D {
		return bson.D{{Key: operator, Value: bson.D{{Key: "$map", Value: bson.D{
			{Key: "input", Value: layers},
			{Key: "in", Value: "$$this." + key},
		}}}}}
	}
	lost := statsOf("packetsLost", "$sum")
	packets := statsOf("packets", "$sum")
	lossPct := cond(gt(add(packets, lost), 0),
		bson.D{{Key: "$round", Value: bson.A{bson.D{{Key: "$multiply", Value: bson.A{
			bson.D{{Key: "$divide", Value: bson.A{lost, add(packets, lost)}}}, 100}}}, 2}}},
		float64(0))
	layerSummaries := bson.D{{Key: "$sortArray", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$map", Value: bson.D{
			{Key: "input", Value: layers},
			{Key: "in", Value: bson.D{
				{Key: "layer", Value: "$$this.layer"}, {Key: "bytes", Value: "$$this.bytes"},
				{Key: "packets", Value: "$$this.packets"}, {Key: "frames", Value: "$$this.frames"},
			}},
		}}}},
		{Key: "sortBy", Value: bson.D{{Key: "layer", Value: 1}}},
	}}}
	publishStats := bson.D{
		{Key: "bytes", Value: statsOf("bytes", "$sum")},
		{Key: "packets", Value: packets},
		{Key: "packetsLost", Value: lost},
		{Key: "packetLossPct", Value: lossPct},
		{Key: "nacks", Value: statsOf("nacks", "$sum")},
		{Key: "plis", Value: statsOf("plis", "$sum")},
		{Key: "firs", Value: statsOf("firs", "$sum")},
		{Key: "rttMaxMs", Value: statsOf("rttMaxMs", "$max")},
		{Key: "jitterMaxMs", Value: statsOf("jitterMaxMs", "$max")},
		{Key: "durationMs", Value: statsOf("durationMs", "$max")},
		{Key: "layers", Value: cond(eq(field("type"), livekit.TrackType_AUDIO.String()), bson.A{}, layerSummaries)},
	}

	f := newFields()
	f.set("subscribers.current", size(difference(emptyArray("internal.subscriberSids"), emptyArray("internal.unsubscribedSids"))))
	f.set("subscribers.total", sizeOf("internal.subscriberSids"))
	f.set("subscribers.failed", sizeOf("internal.failedSubscriberSids"))
	f.set("publishStats", cond(exists("internal.publishStats"), publishStats, remove))
	f.set("durationMs", cond(exists("unpublishedAt"), subtract(field("unpublishedAt"), field("publishedAt")), remove))
	f.set("expiresAt", cond(eq(field("status"), trackUnpublished), add(field("unpublishedAt"), retention.Milliseconds()), nil))
	return f
}

func deriveSubscription(opts Options) *fields {
	retention, _, _ := opts.Retention.Of(schema.RetentionTracks)
	f := newFields()
	f.set("durationMs", cond(and(exists("unsubscribedAt"), exists("subscribedAt")),
		subtract(field("unsubscribedAt"), field("subscribedAt")), remove))
	f.set("expiresAt", cond(eq(field("status"), subscriptionUnsubscribed),
		cond(exists("unsubscribedAt"), add(field("unsubscribedAt"), retention.Milliseconds()), field("expiresAt")),
		cond(eq(field("status"), subscriptionFailed), field("expiresAt"), nil)))
	return f
}
