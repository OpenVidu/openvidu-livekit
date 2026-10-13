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

//go:build integration

// Package testutil starts the MongoDB used by the analytics integration tests (build tag `integration`, Docker
// required).
package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoImage is the MongoDB image of OpenVidu deployments. The official mongo:8.0 image refuses to start on Linux
// kernels 6.19 and newer (SERVER-121912). Override with ANALYTICS_TEST_MONGO_IMAGE.
const MongoImage = "openvidu/mongodb:8.0.29-r1"

const mongodPath = "/opt/bitnami/mongodb/bin/mongod"
const mongoshPath = "/opt/bitnami/mongodb/bin/mongosh"

// StartMongo starts a single-node MongoDB replica set and returns a connected client, terminated when the test ends.
func StartMongo(t *testing.T) *mongo.Client {
	t.Helper()
	ctx := context.Background()

	image := os.Getenv("ANALYTICS_TEST_MONGO_IMAGE")
	if image == "" {
		image = MongoImage
	}

	ctr, err := testcontainers.Run(ctx, image,
		testcontainers.WithEntrypoint(mongodPath),
		testcontainers.WithCmd("--replSet", "rs0", "--bind_ip_all", "--port", "27017", "--dbpath", "/tmp",
			"--wiredTigerCacheSizeGB", "0.25"),
		testcontainers.WithExposedPorts("27017/tcp"),
		testcontainers.WithHostConfigModifier(func(hostConfig *container.HostConfig) {
			hostConfig.Memory = 768 * 1024 * 1024
		}),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("27017/tcp").WithStartupTimeout(time.Minute)),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start MongoDB: %v", err)
	}

	initiate := `rs.initiate({_id: "rs0", members: [{_id: 0, host: "localhost:27017"}]});` +
		`let i = 0; while (!db.hello().isWritablePrimary && i < 100) { sleep(100); i++; }` +
		`if (!db.hello().isWritablePrimary) { quit(1); }`
	code, output, err := ctr.Exec(ctx, []string{mongoshPath, "--quiet", "--eval", initiate})
	if err != nil || code != 0 {
		var log strings.Builder
		if output != nil {
			buffer := make([]byte, 4096)
			n, _ := output.Read(buffer)
			log.Write(buffer[:n])
		}
		t.Fatalf("initiate replica set: exit %d, %v: %s", code, err, log.String())
	}

	endpoint, err := ctr.PortEndpoint(ctx, "27017/tcp", "")
	if err != nil {
		t.Fatalf("MongoDB endpoint: %v", err)
	}
	// The member advertises localhost:27017, unreachable from the host, so the client connects directly.
	client, err := mongo.Connect(options.Client().ApplyURI(fmt.Sprintf("mongodb://%s/?directConnection=true", endpoint)))
	if err != nil {
		t.Fatalf("connect to MongoDB: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })

	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "ping", Value: 1}}).Err(); err != nil {
		t.Fatalf("ping MongoDB: %v", err)
	}
	return client
}
