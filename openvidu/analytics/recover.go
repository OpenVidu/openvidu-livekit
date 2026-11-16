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

package analytics

import (
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/livekit/protocol/logger"
)

// errPanic wraps the error of a recovered panic. It is never transient, so the work that panicked is dropped, not
// retried.
var errPanic = errors.New("analytics: recovered from a panic")

// recoverPanic, deferred, keeps a bug in analytics from crashing the server, as the NaN stats did in 3.9.0. It logs
// the panic with its stack and, when err is not nil, sets it to an error wrapping errPanic.
func recoverPanic(what string, err *error) {
	r := recover()
	if r == nil {
		return
	}
	logger.Errorw("analytics: recovered from a panic in "+what, fmt.Errorf("%v", r), "stack", string(debug.Stack()))
	if err != nil {
		*err = fmt.Errorf("%w in %s: %v", errPanic, what, r)
	}
}

// guarded runs fn and returns its panic as an error.
func guarded(what string, fn func() error) (err error) {
	defer recoverPanic(what, &err)
	return fn()
}
