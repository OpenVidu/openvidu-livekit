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

package openviduconfig

import (
	"time"
)

const DefaultCleanupInterval = 3 * time.Minute

type OpenViduConfig struct {
	Analytics              AnalyticsConfig `yaml:"analytics,omitempty"`
	UseGlobalCpuMonitoring bool            `yaml:"use_global_cpu_monitoring,omitempty"`
	// How often each node removes dead nodes and reconciles the entities they left behind
	CleanupInterval time.Duration `yaml:"cleanup_interval,omitempty"`
}

func (c OpenViduConfig) GetCleanupInterval() time.Duration {
	if c.CleanupInterval <= 0 {
		return DefaultCleanupInterval
	}
	return c.CleanupInterval
}

type AnalyticsConfig struct {
	Enabled       bool          `yaml:"enabled,omitempty"`
	MongoUrl      string        `yaml:"mongo_url,omitempty"`
	Database      string        `yaml:"database,omitempty" default:"openvidu"`
	Interval      time.Duration `yaml:"interval,omitempty"`
	FixerInterval time.Duration `yaml:"fixer_interval,omitempty"`
	Expiration    time.Duration `yaml:"expiration,omitempty"`
}
