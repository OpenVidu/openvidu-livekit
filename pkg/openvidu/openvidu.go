package openvidu

import (
	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/openvidu/analytics"
	"github.com/livekit/protocol/logger"
)

func Start(conf *config.Config) {
	if conf.OpenVidu.Analytics.Enabled {
		// Start analytics
		err := analytics.InitializeAnalytics(conf)
		if err != nil {
			logger.Errorw("failed to start analytics", err)
			panic(err)
		}
		go analytics.Start()
	}
}
