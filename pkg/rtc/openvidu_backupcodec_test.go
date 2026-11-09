// BEGIN OPENVIDU BLOCK
package rtc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
)

// A backup codec announced as livekit-client does (publishAdditionalCodecForTrack): the codec without
// layers, and the layers of its own encodings in req.Layers, with their bitrates and without sizes.
func TestBackupCodecTakesItsOwnLayers(t *testing.T) {
	av1Layers := []*livekit.VideoLayer{
		{Quality: livekit.VideoQuality_LOW, Width: 480, Height: 270, Bitrate: 233_334},
		{Quality: livekit.VideoQuality_MEDIUM, Width: 960, Height: 540, Bitrate: 700_000},
		{Quality: livekit.VideoQuality_HIGH, Width: 1920, Height: 1080, Bitrate: 2_100_000},
	}

	// The track publishes AV1 and declares VP8 as its backup: both codecs get the AV1 layers.
	publish := func(t *testing.T) (*ParticipantImpl, *livekit.TrackInfo) {
		p := newParticipantForTest("test")
		ti := p.addPendingTrack(&livekit.AddTrackRequest{
			Cid:    "cidAV1",
			Type:   livekit.TrackType_VIDEO,
			Source: livekit.TrackSource_CAMERA,
			Width:  1920,
			Height: 1080,
			Layers: av1Layers,
			SimulcastCodecs: []*livekit.SimulcastCodec{
				{Codec: "av1", Cid: "cidAV1"},
				{Codec: "vp8"},
			},
		})
		require.NotNil(t, ti)
		require.Len(t, ti.Codecs, 2)
		p.pendingTracksLock.Lock()
		p.addMediaTrack("cidAV1", ti)
		p.pendingTracksLock.Unlock()
		return p, ti
	}

	layersOf := func(ti *livekit.TrackInfo, mimeType string) (layers [][4]uint32) {
		for _, c := range ti.Codecs {
			if c.MimeType == mimeType {
				for _, l := range c.Layers {
					layers = append(layers, [4]uint32{uint32(l.Quality), l.Width, l.Height, l.Bitrate})
				}
			}
		}
		return layers
	}

	t.Run("simulcast backup keeps the known sizes and takes its bitrates", func(t *testing.T) {
		p, published := publish(t)
		ti := p.addPendingTrack(&livekit.AddTrackRequest{
			Cid:             "cidVP8",
			Type:            livekit.TrackType_VIDEO,
			Sid:             published.Sid,
			SimulcastCodecs: []*livekit.SimulcastCodec{{Codec: "vp8", Cid: "cidVP8"}},
			Layers: []*livekit.VideoLayer{
				{Quality: livekit.VideoQuality_LOW, Bitrate: 160_000},
				{Quality: livekit.VideoQuality_MEDIUM, Bitrate: 450_000},
				{Quality: livekit.VideoQuality_HIGH, Bitrate: 3_000_000},
			},
		})
		require.NotNil(t, ti)
		require.Equal(t, [][4]uint32{
			{uint32(livekit.VideoQuality_LOW), 480, 270, 160_000},
			{uint32(livekit.VideoQuality_MEDIUM), 960, 540, 450_000},
			{uint32(livekit.VideoQuality_HIGH), 1920, 1080, 3_000_000},
		}, layersOf(ti, "video/vp8"), "layers of the VP8 backup")
		require.Equal(t, layersOf(published, "video/av1"), layersOf(ti, "video/av1"), "layers of the AV1 codec")
		for _, l := range ti.Codecs[1].Layers {
			require.Equal(t, []string{"q", "h", "f"}[l.Quality], l.Rid, "rid of the %s layer", l.Quality)
		}
	})

	t.Run("backup with a single layer and sizes takes it as it comes", func(t *testing.T) {
		p, published := publish(t)
		ti := p.addPendingTrack(&livekit.AddTrackRequest{
			Cid:             "cidVP8",
			Type:            livekit.TrackType_VIDEO,
			Sid:             published.Sid,
			SimulcastCodecs: []*livekit.SimulcastCodec{{Codec: "vp8", Cid: "cidVP8"}},
			Layers:          []*livekit.VideoLayer{{Quality: livekit.VideoQuality_HIGH, Width: 1280, Height: 720, Bitrate: 1_500_000}},
		})
		require.NotNil(t, ti)
		require.Equal(t, [][4]uint32{{uint32(livekit.VideoQuality_HIGH), 1280, 720, 1_500_000}}, layersOf(ti, "video/vp8"))
	})

	t.Run("layers of the codec itself win", func(t *testing.T) {
		p, published := publish(t)
		ti := p.addPendingTrack(&livekit.AddTrackRequest{
			Cid:  "cidVP8",
			Type: livekit.TrackType_VIDEO,
			Sid:  published.Sid,
			SimulcastCodecs: []*livekit.SimulcastCodec{{
				Codec:  "vp8",
				Cid:    "cidVP8",
				Layers: []*livekit.VideoLayer{{Quality: livekit.VideoQuality_HIGH, Width: 640, Height: 360, Bitrate: 800_000}},
			}},
			Layers: []*livekit.VideoLayer{{Quality: livekit.VideoQuality_HIGH, Bitrate: 3_000_000}},
		})
		require.NotNil(t, ti)
		require.Equal(t, [][4]uint32{{uint32(livekit.VideoQuality_HIGH), 640, 360, 800_000}}, layersOf(ti, "video/vp8"))
	})
}

// END OPENVIDU BLOCK
