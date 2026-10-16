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

package projection

import "strings"

// Rollup map keys come from fixed vocabularies so that client-reported strings never become document keys.

// osFamily maps ClientInfo.os to windows, macos, linux, android, ios or other.
func osFamily(os string) string {
	value := strings.ToLower(os)
	switch {
	case strings.Contains(value, "windows"):
		return "windows"
	case strings.Contains(value, "android"):
		return "android"
	case strings.Contains(value, "ios"), strings.Contains(value, "iphone"), strings.Contains(value, "ipad"):
		return "ios"
	case strings.Contains(value, "mac"), strings.Contains(value, "os x"), strings.Contains(value, "darwin"):
		return "macos"
	case strings.Contains(value, "linux"), strings.Contains(value, "ubuntu"), strings.Contains(value, "debian"),
		strings.Contains(value, "fedora"), strings.Contains(value, "chrome os"), strings.Contains(value, "chromeos"):
		return "linux"
	default:
		return "other"
	}
}

// browserFamily maps ClientInfo.browser to chrome, firefox, safari, edge or other.
func browserFamily(browser string) string {
	value := strings.ToLower(browser)
	switch {
	case strings.Contains(value, "edg"):
		return "edge"
	case strings.Contains(value, "firefox"):
		return "firefox"
	case strings.Contains(value, "chrom"):
		return "chrome"
	case strings.Contains(value, "safari"):
		return "safari"
	default:
		return "other"
	}
}

// connectionTypeKey maps client_meta.connection_type to udp, tcp, turn or other.
func connectionTypeKey(connectionType string) string {
	switch value := strings.ToLower(connectionType); value {
	case "udp", "tcp", "turn":
		return value
	default:
		return "other"
	}
}
