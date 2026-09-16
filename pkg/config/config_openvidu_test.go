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

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfig_SentinelRedisTimeoutDefaults(t *testing.T) {
	testCases := []struct {
		name      string
		config    string
		wantDial  int
		wantRead  int
		wantWrite int
	}{
		{
			name: "sentinel without timeouts gets the go-redis defaults",
			config: `redis:
  sentinel_master_name: openvidu
  sentinel_addresses:
    - master-node-1:7001`,
			wantDial:  sentinelRedisDefaultDialTimeoutMs,
			wantRead:  sentinelRedisDefaultReadTimeoutMs,
			wantWrite: sentinelRedisDefaultWriteTimeoutMs,
		},
		{
			name: "sentinel keeps an explicit dial timeout",
			config: `redis:
  sentinel_master_name: openvidu
  sentinel_addresses:
    - master-node-1:7001
  dial_timeout: 1500`,
			wantDial:  1500,
			wantRead:  sentinelRedisDefaultReadTimeoutMs,
			wantWrite: sentinelRedisDefaultWriteTimeoutMs,
		},
		{
			name: "sentinel keeps an explicit read timeout",
			config: `redis:
  sentinel_master_name: openvidu
  sentinel_addresses:
    - master-node-1:7001
  read_timeout: 500`,
			wantDial:  sentinelRedisDefaultDialTimeoutMs,
			wantRead:  500,
			wantWrite: sentinelRedisDefaultWriteTimeoutMs,
		},
		{
			name: "sentinel keeps an explicit write timeout",
			config: `redis:
  sentinel_master_name: openvidu
  sentinel_addresses:
    - master-node-1:7001
  write_timeout: 750`,
			wantDial:  sentinelRedisDefaultDialTimeoutMs,
			wantRead:  sentinelRedisDefaultReadTimeoutMs,
			wantWrite: 750,
		},
		{
			name: "sentinel keeps all explicit timeouts",
			config: `redis:
  sentinel_master_name: openvidu
  sentinel_addresses:
    - master-node-1:7001
  dial_timeout: 1500
  read_timeout: 500
  write_timeout: 750`,
			wantDial:  1500,
			wantRead:  500,
			wantWrite: 750,
		},
		{
			// A negative value is how go-redis disables a timeout; the "== 0" guard
			// must leave it untouched instead of overriding it with a default.
			name: "sentinel keeps a negative timeout used to disable it",
			config: `redis:
  sentinel_master_name: openvidu
  sentinel_addresses:
    - master-node-1:7001
  read_timeout: -1`,
			wantDial:  sentinelRedisDefaultDialTimeoutMs,
			wantRead:  -1,
			wantWrite: sentinelRedisDefaultWriteTimeoutMs,
		},
		{
			name: "plain address keeps the client defaults",
			config: `redis:
  address: localhost:6379`,
			wantDial:  0,
			wantRead:  0,
			wantWrite: 0,
		},
		{
			name: "cluster mode keeps the client defaults",
			config: `redis:
  cluster_addresses:
    - node-1:7001
    - node-2:7002`,
			wantDial:  0,
			wantRead:  0,
			wantWrite: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			conf, err := NewConfig(tc.config, true, nil, nil)
			require.NoError(t, err)
			require.Equal(t, tc.wantDial, conf.Redis.DialTimeout)
			require.Equal(t, tc.wantRead, conf.Redis.ReadTimeout)
			require.Equal(t, tc.wantWrite, conf.Redis.WriteTimeout)
		})
	}
}
