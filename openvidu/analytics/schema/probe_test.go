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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestClassifyPipelineProbe(t *testing.T) {
	rejected := mongo.CommandError{Code: 14, Message: "Wrong type for parameter u"}
	network := errors.New("connection reset")

	require.NoError(t, classifyPipelineProbe(nil, nil))
	require.ErrorIs(t, classifyPipelineProbe(nil, rejected), ErrPipelineUpdatesUnsupported)
	require.ErrorContains(t, classifyPipelineProbe(nil, rejected), "Wrong type for parameter u")

	// A database that cannot explain updates at all, or a failure that is not a rejection, says nothing about pipelines.
	require.NotErrorIs(t, classifyPipelineProbe(rejected, rejected), ErrPipelineUpdatesUnsupported)
	require.NotErrorIs(t, classifyPipelineProbe(nil, network), ErrPipelineUpdatesUnsupported)
}
