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

package schema

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestTimeConversions(t *testing.T) {
	instant := time.Date(2026, 10, 12, 9, 0, 5, 123_456_789, time.UTC)
	millis := instant.Truncate(time.Millisecond)

	tests := []struct {
		name     string
		got      time.Time
		expected time.Time
	}{
		{"seconds", UnixSeconds(instant.Unix()), instant.Truncate(time.Second)},
		{"milliseconds", UnixMillis(instant.UnixMilli()), millis},
		{"nanoseconds", UnixNanos(instant.UnixNano()), millis},
		{"timestamp", FromTimestamp(timestamppb.New(instant)), millis},
		{"zero seconds", UnixSeconds(0), time.Time{}},
		{"zero milliseconds", UnixMillis(0), time.Time{}},
		{"zero nanoseconds", UnixNanos(0), time.Time{}},
		{"nil timestamp", FromTimestamp(nil), time.Time{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.True(t, test.expected.Equal(test.got), "expected %s, got %s", test.expected, test.got)
			if !test.got.IsZero() {
				require.Equal(t, time.UTC, test.got.Location())
			}
		})
	}
}

func TestDurationConversions(t *testing.T) {
	start := UnixNanos(time.Date(2026, 10, 12, 9, 2, 0, 0, time.UTC).UnixNano())
	end := UnixNanos(time.Date(2026, 10, 12, 9, 40, 20, 999_999_999, time.UTC).UnixNano())

	require.Equal(t, int64(2_300_999), DurationMs(start, end), "egress nanoseconds are exact to the millisecond")
	require.Zero(t, DurationMs(time.Time{}, end))
	require.Zero(t, DurationMs(end, start))
	require.Equal(t, int64(2_294_000), NanosToMs(2_294_000_000_000))
	require.Equal(t, int64(300_000), SecondsToMs(300))
	require.Equal(t, 2.5, MicrosToMs(2500))
}
