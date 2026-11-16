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

//go:build integration

package analytics

import (
	"context"
	"math"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/mono"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/livekit/livekit-server/openvidu/analytics/schema"
	"github.com/livekit/livekit-server/openvidu/analytics/testutil"
	"github.com/livekit/livekit-server/openvidu/goutil"
	"github.com/livekit/livekit-server/openvidu/openviduconfig"
	"github.com/livekit/livekit-server/pkg/sfu/rtpstats"
)

// subscriberStatsAfterStartAdjust returns the subscriber stats that crashed 3.9.0: the second packet forwarded came
// before the first one ("adjusting start sequence number"), which leaves 0 packets, and with one RTX retransmission
// the loss percentage is 0/0.
func subscriberStatsAfterStartAdjust() *livekit.RTPStats {
	primary := rtpstats.NewRTPStatsSender(rtpstats.RTPStatsParams{}, 8192)
	primary.SetClockRate(90000)
	rtx := rtpstats.NewRTPStatsSender(rtpstats.RTPStatsParams{IsRTX: true}, 8192)
	rtx.SetClockRate(90000)

	now := mono.UnixNano()
	primary.Update(now, 9844, 3793230347, false, 20, 1131, 0, false)
	primary.Update(now+162_000, 9843, 3793230295, false, 20, 1131, 0, true)
	for sn := uint64(9845); sn < 9900; sn++ {
		primary.Update(now+int64(sn-9843)*1_000_000, sn, 3793230347+(sn-9844)*3000, false, 20, 1100, 0, false)
	}
	rtx.Update(now+50_000_000, 1, 3793230347, false, 20, 1100, 0, false)
	return rtpstats.ReconcileRTPStatsWithRTX(primary.ToProto(), rtx.ToProto())
}

// setFloats sets every float and double field of m to v, and recurses into the messages that are set. Oneof
// members are left alone so the message keeps its shape. It returns how many fields it set.
func setFloats(m protoreflect.Message, v float64) int {
	set := 0
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		isFloat := f.Kind() == protoreflect.FloatKind || f.Kind() == protoreflect.DoubleKind
		isMessage := f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind
		inOneof := f.ContainingOneof() != nil && !f.ContainingOneof().IsSynthetic()
		value := protoreflect.ValueOfFloat64(v)
		if f.Kind() == protoreflect.FloatKind {
			value = protoreflect.ValueOfFloat32(float32(v))
		}
		switch {
		case f.IsMap():
			if f.MapValue().Kind() == protoreflect.MessageKind && m.Has(f) {
				m.Mutable(f).Map().Range(func(_ protoreflect.MapKey, item protoreflect.Value) bool {
					set += setFloats(item.Message(), v)
					return true
				})
			}
		case f.IsList() && isMessage:
			if m.Has(f) {
				list := m.Mutable(f).List()
				for j := 0; j < list.Len(); j++ {
					set += setFloats(list.Get(j).Message(), v)
				}
			}
		case f.IsList() && isFloat:
			m.Mutable(f).List().Append(value)
			set++
		case isFloat && !inOneof:
			m.Set(f, value)
			set++
		case isMessage && m.Has(f):
			set += setFloats(m.Mutable(f).Message(), v)
		}
	}
	return set
}

// statsLifecycle is a room with a camera and a microphone, subscribed and unsubscribed, whose final stats are the
// ones that crashed 3.9.0.
func statsLifecycle() ([]*livekit.AnalyticsEvent, []*livekit.AnalyticsStat) {
	var ev events
	pub := participantInfo("PA_pub", "pub", livekit.ParticipantInfo_STANDARD, at("1s"))
	sub := participantInfo("PA_sub", "sub", livekit.ParticipantInfo_STANDARD, at("1s"))
	cam, mic := cameraTrack("TR_cam"), microphoneTrack("TR_mic")

	publishStats := ev.track(livekit.AnalyticsEventType_TRACK_PUBLISH_STATS, "RM_1", pub, cam, at("50s"))
	publishStats.RtpStats = subscriberStatsAfterStartAdjust()
	publishStats.VideoLayer = 2
	videoStats := ev.subscription(livekit.AnalyticsEventType_TRACK_SUBSCRIBE_STATS, "RM_1", "PA_sub", cam, pub, at("51s"))
	videoStats.RtpStats = subscriberStatsAfterStartAdjust()
	videoStats.Mime = "video/VP8"
	audioStats := ev.subscription(livekit.AnalyticsEventType_TRACK_SUBSCRIBE_STATS, "RM_1", "PA_sub", mic, pub, at("51s"))
	audioStats.RtpStats = subscriberStatsAfterStartAdjust()

	batch := []*livekit.AnalyticsEvent{
		ev.roomCreated("RM_1", "r", at("0s")),
		ev.joined("RM_1", pub, at("1s")), ev.active("RM_1", pub, at("2s")),
		ev.joined("RM_1", sub, at("1s")), ev.active("RM_1", sub, at("2s")),
		ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_1", pub, cam, at("3s")),
		ev.track(livekit.AnalyticsEventType_TRACK_PUBLISHED, "RM_1", pub, mic, at("3s")),
		ev.subscription(livekit.AnalyticsEventType_TRACK_SUBSCRIBED, "RM_1", "PA_sub", cam, pub, at("4s")),
		ev.subscription(livekit.AnalyticsEventType_TRACK_SUBSCRIBED, "RM_1", "PA_sub", mic, pub, at("4s")),
		publishStats, videoStats, audioStats,
		ev.subscription(livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED, "RM_1", "PA_sub", cam, pub, at("52s")),
		ev.subscription(livekit.AnalyticsEventType_TRACK_UNSUBSCRIBED, "RM_1", "PA_sub", mic, pub, at("52s")),
		ev.track(livekit.AnalyticsEventType_TRACK_UNPUBLISHED, "RM_1", pub, cam, at("53s")),
		ev.track(livekit.AnalyticsEventType_TRACK_UNPUBLISHED, "RM_1", pub, mic, at("53s")),
		ev.left("RM_1", sub, at("54s"), livekit.DisconnectReason_CLIENT_INITIATED),
		ev.left("RM_1", pub, at("55s"), livekit.DisconnectReason_CLIENT_INITIATED),
		ev.roomEnded("RM_1", "r", at("0s"), at("1m"), livekit.RoomEndReason_ROOM_END_API_DELETE),
	}
	stats := []*livekit.AnalyticsStat{
		stat("RM_1", "PA_pub", "TR_cam", livekit.StreamType_UPSTREAM, at("10s"), 4.5, 1000),
		stat("RM_1", "PA_sub", "TR_cam", livekit.StreamType_DOWNSTREAM, at("10s"), 4.5, 1000),
		stat("RM_1", "PA_pub", "TR_cam", livekit.StreamType_UPSTREAM, at("20s"), 4.5, 2000),
		stat("RM_1", "PA_sub", "TR_cam", livekit.StreamType_DOWNSTREAM, at("20s"), 4.5, 2000),
	}
	return batch, stats
}

// TestNonFiniteFloats is the regression test of the 3.9.0 crash: NaN and infinite floats in events and stats are
// written, reconciled and aggregated without a panic and without a rejected write.
func TestNonFiniteFloats(t *testing.T) {
	require.True(t, math.IsNaN(float64(subscriberStatsAfterStartAdjust().GetPacketLossPercentage())),
		"LiveKit still computes a NaN loss percentage for these stats")

	client := testutil.StartMongo(t)
	cases := []struct {
		name       string
		everyFloat *float64
	}{
		{"the stats of the crash", nil},
		{"NaN in every float", goutil.Ptr(math.NaN())},
		{"+Inf in every float", goutil.Ptr(math.Inf(1))},
		{"-Inf in every float", goutil.Ptr(math.Inf(-1))},
	}
	for _, detail := range []string{"track", "participant"} {
		for _, c := range cases {
			t.Run(detail+"/"+c.name, func(t *testing.T) {
				env := newAggregatorEnv(t, client, func(conf *openviduconfig.AnalyticsConfig) { conf.StatsDetail = detail })
				batch, stats := statsLifecycle()
				if c.everyFloat != nil {
					set := 0
					for _, event := range batch {
						set += setFloats(event.ProtoReflect(), *c.everyFloat)
					}
					for _, s := range stats {
						set += setFloats(s.ProtoReflect(), *c.everyFloat)
					}
					require.Greater(t, set, 50)
				}

				require.NotPanics(t, func() { env.apply(batch, stats) })
				require.NotPanics(t, func() { env.apply(batch, stats) }, "the same batch again, as after a retry")
				require.NotPanics(t, func() { env.reconcile() })
				require.NotPanics(t, func() { env.aggregate() })

				require.EqualValues(t, 3, count(env.testEnv, schema.CollectionEvents, bson.D{{Key: "type", Value: bson.D{
					{Key: "$in", Value: bson.A{"TRACK_PUBLISH_STATS", "TRACK_SUBSCRIBE_STATS"}}}}}))
				require.Equal(t, "closed", find[schema.RoomDocument](env.testEnv, schema.CollectionRooms, "RM_1").Status)

				var subscription struct {
					SubscribeStats *struct {
						PacketLossPct float64 `bson:"packetLossPct"`
					} `bson:"subscribeStats"`
				}
				require.NoError(t, env.db.Collection(schema.CollectionTrackSubscriptions).FindOne(context.Background(),
					bson.D{{Key: "trackSid", Value: "TR_cam"}}).Decode(&subscription))
				require.NotNil(t, subscription.SubscribeStats)
				loss := subscription.SubscribeStats.PacketLossPct
				require.False(t, math.IsNaN(loss) || math.IsInf(loss, 0), "the loss percentage is computed, not copied")
			})
		}
	}
}
