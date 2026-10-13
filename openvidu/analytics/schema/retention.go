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
	"fmt"
	"time"
)

// RetentionClass names a configurable retention of the data model.
type RetentionClass string

const (
	RetentionEvents        RetentionClass = "events"
	RetentionStats         RetentionClass = "stats"
	RetentionTracks        RetentionClass = "tracks"
	RetentionEntities      RetentionClass = "entities"
	RetentionRollupsHourly RetentionClass = "rollupsHourly"
	RetentionRollupsDaily  RetentionClass = "rollupsDaily"
)

const day = 24 * time.Hour

// Retention holds the retention of every class. A zero RollupsDaily keeps daily rollups forever.
type Retention struct {
	Events        time.Duration
	Stats         time.Duration
	Tracks        time.Duration
	Entities      time.Duration
	RollupsHourly time.Duration
	RollupsDaily  time.Duration
}

// DefaultRetention returns the contract defaults: events, stats and tracks 7 days, entities 90 days, hourly rollups
// 400 days and daily rollups forever.
func DefaultRetention() Retention {
	return Retention{
		Events:        7 * day,
		Stats:         7 * day,
		Tracks:        7 * day,
		Entities:      90 * day,
		RollupsHourly: 400 * day,
	}
}

// Of returns the retention of a class; ok is false when documents of the class are kept forever.
func (r Retention) Of(class RetentionClass) (retention time.Duration, ok bool, err error) {
	switch class {
	case RetentionEvents:
		retention = r.Events
	case RetentionStats:
		retention = r.Stats
	case RetentionTracks:
		retention = r.Tracks
	case RetentionEntities:
		retention = r.Entities
	case RetentionRollupsHourly:
		retention = r.RollupsHourly
	case RetentionRollupsDaily:
		retention = r.RollupsDaily
	default:
		return 0, false, fmt.Errorf("unknown retention class %q", class)
	}
	return retention, retention > 0, nil
}
