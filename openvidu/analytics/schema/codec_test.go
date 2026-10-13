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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

const sentinelPrefix = "SENTINEL:"

var sentinelTime = time.Date(2026, 10, 12, 9, 0, 5, 123_456_789, time.UTC)

// populate sets every field of a message, recursively up to a depth, with values that are never zero. Strings and
// bytes hold `SENTINEL:<message>.<field>` so a surviving value names its source field; in every oneof the member at
// position `variant` modulo the number of members is the one set.
func populate(message protoreflect.Message, variant int, depth int) {
	descriptor := message.Descriptor()
	if descriptor.FullName() == timestampName {
		message.Set(descriptor.Fields().ByName("seconds"), protoreflect.ValueOfInt64(sentinelTime.Unix()))
		message.Set(descriptor.Fields().ByName("nanos"), protoreflect.ValueOfInt32(int32(sentinelTime.Nanosecond())))
		return
	}
	if depth == 0 {
		return
	}

	fields := descriptor.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if oneof := field.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
			members := oneof.Fields()
			if members.Get(variant%members.Len()).Number() != field.Number() {
				continue
			}
		}
		sentinel := fmt.Sprintf("%s%s.%s", sentinelPrefix, descriptor.FullName(), field.Name())

		switch {
		case field.IsList():
			list := message.Mutable(field).List()
			list.Append(sampleValue(list.NewElement, field, sentinel, variant, depth))
		case field.IsMap():
			entries := message.Mutable(field).Map()
			key := protoreflect.ValueOfString(sentinel + ":key").MapKey()
			if field.MapKey().Kind() != protoreflect.StringKind {
				key = protoreflect.ValueOfInt32(1).MapKey()
			}
			entries.Set(key, sampleValue(entries.NewValue, field.MapValue(), sentinel, variant, depth))
		case field.Kind() == protoreflect.MessageKind:
			populate(message.Mutable(field).Message(), variant, depth-1)
		default:
			message.Set(field, sampleValue(nil, field, sentinel, variant, depth))
		}
	}
}

func sampleValue(newValue func() protoreflect.Value, field protoreflect.FieldDescriptor, sentinel string, variant int,
	depth int) protoreflect.Value {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(true)
	case protoreflect.EnumKind:
		values := field.Enum().Values()
		return protoreflect.ValueOfEnum(values.Get(values.Len() - 1).Number())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(7)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(7)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(sentinelTime.UnixNano())
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(7)
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(0.05)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(0.5)
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(sentinel)
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte(sentinel))
	default:
		value := newValue()
		populate(value.Message(), variant, depth-1)
		return value
	}
}

// survivors returns the sentinels found in an encoded document, and fails on any time not in UTC or not truncated
// to milliseconds and on any timestamp left as a {seconds, nanos} document.
func survivors(t *testing.T, value any) map[string]bool {
	found := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case bson.D:
			keys := map[string]bool{}
			for _, element := range v {
				keys[element.Key] = true
				if strings.HasPrefix(element.Key, sentinelPrefix) {
					found[strings.TrimSuffix(element.Key, ":key")] = true
				}
				walk(element.Value)
			}
			require.False(t, keys["seconds"] && keys["nanos"], "timestamp encoded as a document: %v", v)
		case bson.A:
			for _, item := range v {
				walk(item)
			}
		case string:
			if strings.HasPrefix(v, sentinelPrefix) {
				found[v] = true
			}
		case bson.Binary:
			if strings.HasPrefix(string(v.Data), sentinelPrefix) {
				found[string(v.Data)] = true
			}
		case time.Time:
			require.Equal(t, time.UTC, v.Location())
			require.True(t, v.Equal(sentinelTime.Truncate(time.Millisecond)), "time %s not truncated to ms", v)
		}
	}
	walk(value)
	return found
}

// secretFields are fields that must never be stored, whatever the allow-list says.
var secretFields = regexp.MustCompile(`(?i)(password|secret|token|credentials|stream_key|access_key|account_key|` +
	`signing_key|api_key|analytics_key|private_key|external_id|session_token|from_uri|to_uri)`)

func fullyPopulated(t *testing.T, name protoreflect.FullName, variant int) proto.Message {
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(name)
	require.NoError(t, err)
	message := messageType.New()
	populate(message, variant, 6)
	return message.Interface()
}

func TestAllowListMatchesTheProtocol(t *testing.T) {
	for name, rules := range allowList {
		descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
		require.NoError(t, err, name)
		messageDescriptor, ok := descriptor.(protoreflect.MessageDescriptor)
		require.True(t, ok, name)

		for fieldName := range rules {
			field := messageDescriptor.Fields().ByName(fieldName)
			require.NotNil(t, field, "%s.%s does not exist", name, fieldName)
			require.True(t, encodable(field), "%s.%s has a message type missing from the allow-list", name, fieldName)
			require.False(t, secretFields.MatchString(string(fieldName)), "%s.%s looks like a secret", name, fieldName)
		}
	}
}

func TestEncodeKeepsNoSecret(t *testing.T) {
	messages := []protoreflect.FullName{
		"livekit.AnalyticsEvent", "livekit.Room", "livekit.ParticipantInfo", "livekit.EgressInfo",
		"livekit.IngressInfo", "livekit.SIPCallInfo",
	}
	egressRequests := (&livekit.EgressInfo{}).ProtoReflect().Descriptor().Oneofs().ByName("request").Fields().Len()

	for _, name := range messages {
		for variant := 0; variant < egressRequests; variant++ {
			for _, opts := range []EncodeOptions{{}, {StoreMetadata: true}, {AnonymizeIP: true}} {
				t.Run(fmt.Sprintf("%s/%d/%+v", name, variant, opts), func(t *testing.T) {
					found := survivors(t, Encode(fullyPopulated(t, name, variant), opts))
					require.NotEmpty(t, found)

					for sentinel := range found {
						field := strings.TrimPrefix(sentinel, sentinelPrefix)
						require.False(t, secretFields.MatchString(field), "secret %s was stored", field)

						cut := strings.LastIndex(field, ".")
						rule, listed := allowList[protoreflect.FullName(field[:cut])][protoreflect.Name(field[cut+1:])]
						require.True(t, listed, "%s is not in the allow-list", field)
						require.True(t, rule != metadata || opts.StoreMetadata, "metadata %s stored without store_metadata", field)
						require.True(t, rule != clientAddress || !opts.AnonymizeIP, "address %s stored with anonymize_ip", field)
					}
				})
			}
		}
	}
}

func TestEncodeEveryEgressRequestVariant(t *testing.T) {
	members := (&livekit.EgressInfo{}).ProtoReflect().Descriptor().Oneofs().ByName("request").Fields()
	for variant := 0; variant < members.Len(); variant++ {
		document := Encode(fullyPopulated(t, "livekit.EgressInfo", variant), EncodeOptions{})
		request, ok := lookup(document, "request").(bson.D)
		require.True(t, ok, "variant %d has no request document", variant)
		require.Len(t, request, 1)
		require.Equal(t, string(members.Get(variant).Name()), request[0].Key)
	}
}

func TestEncodeOmitsUnsetFields(t *testing.T) {
	document := Encode(&livekit.TrackInfo{Sid: "TR_x"}, EncodeOptions{})
	require.Equal(t, bson.D{{Key: "sid", Value: "TR_x"}}, document)
}

func TestEncodeTypes(t *testing.T) {
	document := Encode(&livekit.EgressInfo{
		EgressId:  "EG_x",
		Status:    livekit.EgressStatus_EGRESS_COMPLETE,
		StartedAt: sentinelTime.UnixNano(),
		FileResults: []*livekit.FileInfo{{
			Filename: "a.mp4",
			Size:     3_000_000_000,
		}},
	}, EncodeOptions{})

	require.Equal(t, "EGRESS_COMPLETE", lookup(document, "status"))
	require.Equal(t, sentinelTime.UnixNano(), lookup(document, "started_at"))
	results := lookup(document, "file_results").(bson.A)
	require.Equal(t, int64(3_000_000_000), lookup(results[0].(bson.D), "size"))

	stats := Encode(&livekit.RTPStats{PacketLossPercentage: 0.05, Packets: 7}, EncodeOptions{})
	require.Equal(t, 0.05, lookup(stats, "packet_loss_percentage"))
	require.Equal(t, int32(7), lookup(stats, "packets"))
}

func TestStreamURLRedacted(t *testing.T) {
	document := Encode(&livekit.EgressInfo{
		Request: &livekit.EgressInfo_RoomComposite{RoomComposite: &livekit.RoomCompositeEgressRequest{
			StreamOutputs: []*livekit.StreamOutput{{Urls: []string{"rtmps://user:pw@live.example.com/app/live_123?token=abc"}}},
		}},
		StreamResults: []*livekit.StreamInfo{{Url: "rtmp://live.example.com/live/key123"}},
	}, EncodeOptions{})

	request := lookup(lookup(document, "request").(bson.D), "room_composite").(bson.D)
	outputs := lookup(request, "stream_outputs").(bson.A)
	require.Equal(t, bson.A{"rtmps://live.example.com/app/***"}, lookup(outputs[0].(bson.D), "urls"))
	results := lookup(document, "stream_results").(bson.A)
	require.Equal(t, "rtmp://live.example.com/live/***", lookup(results[0].(bson.D), "url"))
}

func TestURLHelpers(t *testing.T) {
	require.Equal(t, "rtmps://live.example.com/app/***", RedactURL("rtmps://user:pw@live.example.com/app/live_123?token=abc"))
	require.Equal(t, "https://example.com/***", RedactURL("https://example.com/page?jwt=x#y"))
	require.Equal(t, "https://example.com", RedactURL("https://example.com"))
	require.Equal(t, "***", RedactURL("not a url"))
	require.Equal(t, "rtmp://ingress.example.com/x", StripURLCredentials("rtmp://u:p@ingress.example.com/x?k=v"))
}

func TestPersonalDataFlags(t *testing.T) {
	event := &livekit.AnalyticsEvent{
		Type:        livekit.AnalyticsEventType_PARTICIPANT_JOINED,
		Participant: &livekit.ParticipantInfo{Sid: "PA_x", Metadata: "m", Attributes: map[string]string{"a": "b"}},
		ClientInfo:  &livekit.ClientInfo{Address: "203.0.113.10"},
		ClientMeta:  &livekit.AnalyticsClientMeta{ClientAddr: "203.0.113.10"},
		Room:        &livekit.Room{Sid: "RM_x", Metadata: "room", TurnPassword: "turn"},
	}

	defaults := Encode(event, EncodeOptions{})
	require.Equal(t, "203.0.113.10", lookup(lookup(defaults, "client_info").(bson.D), "address"))
	require.Nil(t, lookup(lookup(defaults, "participant").(bson.D), "metadata"))
	require.Nil(t, lookup(lookup(defaults, "participant").(bson.D), "attributes"))
	require.Nil(t, lookup(lookup(defaults, "room").(bson.D), "metadata"))
	require.Nil(t, lookup(lookup(defaults, "room").(bson.D), "turn_password"))
	require.Nil(t, lookup(defaults, "type"), "lifted keys stay out of the payload")

	anonymized := Encode(event, EncodeOptions{AnonymizeIP: true, StoreMetadata: true})
	require.Nil(t, lookup(lookup(anonymized, "client_info").(bson.D), "address"))
	require.Nil(t, lookup(lookup(anonymized, "client_meta").(bson.D), "client_addr"))
	require.Equal(t, "m", lookup(lookup(anonymized, "participant").(bson.D), "metadata"))
	require.Equal(t, "room", lookup(lookup(anonymized, "room").(bson.D), "metadata"))
}

func lookup(document bson.D, key string) any {
	for _, element := range document {
		if element.Key == key {
			return element.Value
		}
	}
	return nil
}
