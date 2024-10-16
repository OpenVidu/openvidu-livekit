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

type OpenViduConfig struct {
	Analytics AnalyticsConfig `yaml:"analytics"`
}

type AnalyticsConfig struct {
	Enabled    bool          `yaml:"enabled"`
	MongoUrl   string        `yaml:"mongo_url"`
	Interval   time.Duration `yaml:"interval"`
	Expiration time.Duration `yaml:"expiration"`
}
