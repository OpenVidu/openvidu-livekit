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
	"runtime"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/livekit-server/pkg/telemetry/prometheus"
	"github.com/livekit/protocol/logger"

	"github.com/OpenVidu/openvidu-golang-utils/monitor"
	"github.com/livekit/livekit-server/openvidu/analytics"
	"github.com/livekit/livekit-server/openvidu/livekithelper"
)

func Start(conf *config.Config, server *service.LivekitServer) {
	if conf.OpenVidu.UseGlobalCpuMonitoring {
		startGlobalCPUMonitor()
	}

	if conf.OpenVidu.Analytics.Enabled {
		// Start livekit helper
		livekithelper.Init(server)
		// Start analytics
		err := analytics.InitializeAnalytics(conf, livekithelper.GetInstance())
		if err != nil {
			logger.Errorw("failed to start analytics", err)
			panic(err)
		}
		go analytics.Start()
	}
}

func startGlobalCPUMonitor() {
	m := monitor.NewMonitor(monitor.WithLogger(logger.GetLogger()))
	m.Start()
	numCPU := float64(runtime.NumCPU())
	prometheus.SetHostCPULoadFunc(func() float32 {
		return float32(1 - m.GetHostCpuIdle()/numCPU)
	})
	logger.Infow("global CPU monitoring enabled. Using host-wide CPU load")
}
