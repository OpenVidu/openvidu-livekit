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
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// Unit conversions from the protocol's mixed units to the stored ones: UTC times with millisecond precision (the
// precision of a BSON date), integer milliseconds for durations and float milliseconds for jitter. A zero input
// yields the zero time, which callers treat as unknown.

// UnixSeconds converts seconds since the epoch (Room.creation_time, ParticipantInfo.joined_at).
func UnixSeconds(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

// UnixMillis converts milliseconds since the epoch (Room.creation_time_ms, ParticipantInfo.joined_at_ms).
func UnixMillis(milliseconds int64) time.Time {
	if milliseconds == 0 {
		return time.Time{}
	}
	return time.UnixMilli(milliseconds).UTC()
}

// UnixNanos converts nanoseconds since the epoch (egress and ingress timestamps), truncated to milliseconds.
func UnixNanos(nanoseconds int64) time.Time {
	if nanoseconds == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanoseconds).UTC().Truncate(time.Millisecond)
}

// FromTimestamp converts a protobuf timestamp (AnalyticsEvent.timestamp, RTPStats times), truncated to milliseconds.
func FromTimestamp(timestamp *timestamppb.Timestamp) time.Time {
	if timestamp == nil || (timestamp.GetSeconds() == 0 && timestamp.GetNanos() == 0) {
		return time.Time{}
	}
	return timestamp.AsTime().UTC().Truncate(time.Millisecond)
}

// DurationMs returns end - start in whole milliseconds; 0 when either end is unknown or the interval is negative.
func DurationMs(start time.Time, end time.Time) int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

// NanosToMs converts a nanosecond duration (FileInfo.duration, SegmentsInfo.duration) to whole milliseconds.
func NanosToMs(nanoseconds int64) int64 {
	return nanoseconds / int64(time.Millisecond)
}

// SecondsToMs converts a second duration (Room.empty_timeout, Room.departure_timeout) to milliseconds.
func SecondsToMs(seconds uint32) int64 {
	return int64(seconds) * 1000
}

// MicrosToMs converts a microsecond value (AnalyticsStream.jitter) to fractional milliseconds.
func MicrosToMs(microseconds uint32) float64 {
	return float64(microseconds) / 1000
}
