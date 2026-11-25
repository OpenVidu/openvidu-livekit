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
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// IsTransient reports errors a later retry can overcome: network errors, timeouts, elections, shutdowns, write concern
// and the KeyNotFound (211) of a restarted replica set that has not loaded its signing keys yet, returned during the
// authentication handshake.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if mongo.IsNetworkError(err) || mongo.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var serverErr mongo.ServerError
	if errors.As(err, &serverErr) {
		if serverErr.HasErrorLabel("RetryableWriteError") || serverErr.HasErrorLabel("TransientTransactionError") {
			return true
		}
		for _, code := range []int{6, 7, 89, 91, 189, 211, 262, 9001, 10107, 11600, 11602, 13435, 13436, 64} {
			if serverErr.HasErrorCode(code) {
				return true
			}
		}
	}
	var bulkErr mongo.BulkWriteException
	if errors.As(err, &bulkErr) && bulkErr.WriteConcernError != nil {
		return true
	}
	return false
}
