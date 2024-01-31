// Copyright 2024 OpenVidu
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

package openvidu

import (
	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/protocol/logger"

	"github.com/openvidu/openvidu-livekit/openvidu/analytics"
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
