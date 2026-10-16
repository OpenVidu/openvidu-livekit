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
	"context"
	"fmt"
	"runtime"

	"github.com/livekit/livekit-server/pkg/config"
	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/livekit-server/pkg/telemetry"
	"github.com/livekit/livekit-server/pkg/telemetry/prometheus"
	"github.com/livekit/protocol/logger"

	"github.com/OpenVidu/openvidu-golang-utils/monitor"
	"github.com/livekit/livekit-server/openvidu/analytics"
	"github.com/livekit/livekit-server/openvidu/livekithelper"
)

var analyticsEnabled bool

// Start starts the OpenVidu services of the server. An error means the server must not start.
func Start(conf *config.Config, server *service.LivekitServer) error {
	if conf.OpenVidu.UseGlobalCpuMonitoring {
		startGlobalCPUMonitor()
	}

	if conf.OpenVidu.Analytics.Enabled {
		livekithelper.Init(server)
		node := server.Node()
		err := analytics.InitializeAnalytics(conf, analytics.Node{ID: node.GetId(), Region: node.GetRegion()},
			livekithelper.GetInstance())
		if err != nil {
			return fmt.Errorf("failed to start analytics: %w", err)
		}
		analyticsEnabled = true
		go analytics.Start()
	}
	return nil
}

// Stop runs after the server has stopped and closed its rooms: it flushes the telemetry into the analytics writer
// and writes the queued events and stats, waiting at most analytics.ShutdownTimeout.
func Stop() {
	if !analyticsEnabled {
		return
	}
	telemetry.FlushAndStop()
	ctx, cancel := context.WithTimeout(context.Background(), analytics.ShutdownTimeout)
	defer cancel()
	analytics.Stop(ctx)
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
