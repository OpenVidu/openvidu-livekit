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
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/protojson"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

const sentinelPrefix = "SENTINEL:"

var sentinelTime = time.Date(2026, 10, 12, 9, 0, 5, 123_456_789, time.UTC)

// oneofPicker chooses the member set in a oneof of n members.
type oneofPicker func(n int) int

// byVariant sets the member at position variant modulo n in every oneof.
func byVariant(variant int) oneofPicker {
	return func(n int) int { return variant % n }
}

// randomly chooses each oneof member independently, deterministically for a seed.
func randomly(seed uint64) oneofPicker {
	random := rand.New(rand.NewPCG(seed, seed))
	return func(n int) int { return random.IntN(n) }
}

var (
	anyName      = (&anypb.Any{}).ProtoReflect().Descriptor().FullName()
	durationName = (&durationpb.Duration{}).ProtoReflect().Descriptor().FullName()
)

// populate sets every field of a message, recursively up to a depth, with values that are never zero. Strings and
// bytes hold `SENTINEL:<message>.<field>` so a surviving value names its source field; pick chooses the member set in
// each oneof.
func populate(message protoreflect.Message, pick oneofPicker, depth int) {
	descriptor := message.Descriptor()
	switch descriptor.FullName() {
	case timestampName:
		message.Set(descriptor.Fields().ByName("seconds"), protoreflect.ValueOfInt64(sentinelTime.Unix()))
		message.Set(descriptor.Fields().ByName("nanos"), protoreflect.ValueOfInt32(int32(sentinelTime.Nanosecond())))
		return
	case anyName:
		// protojson resolves the type of an Any and bounds a Duration; neither is ever stored.
		message.Set(descriptor.Fields().ByName("type_url"), protoreflect.ValueOfString("type.googleapis.com/google.protobuf.Empty"))
		return
	case durationName:
		message.Set(descriptor.Fields().ByName("seconds"), protoreflect.ValueOfInt64(7))
		return
	}
	if depth == 0 {
		return
	}

	chosen := map[protoreflect.Name]protoreflect.FieldNumber{}
	for i := 0; i < descriptor.Oneofs().Len(); i++ {
		if oneof := descriptor.Oneofs().Get(i); !oneof.IsSynthetic() {
			chosen[oneof.Name()] = oneof.Fields().Get(pick(oneof.Fields().Len())).Number()
		}
	}

	fields := descriptor.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if oneof := field.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() && chosen[oneof.Name()] != field.Number() {
			continue
		}
		sentinel := fmt.Sprintf("%s%s.%s", sentinelPrefix, descriptor.FullName(), field.Name())

		switch {
		case field.IsList():
			list := message.Mutable(field).List()
			list.Append(sampleValue(list.NewElement, field, sentinel, pick, depth))
		case field.IsMap():
			entries := message.Mutable(field).Map()
			key := protoreflect.ValueOfString(sentinel + ":key").MapKey()
			if field.MapKey().Kind() != protoreflect.StringKind {
				key = protoreflect.ValueOfInt32(1).MapKey()
			}
			entries.Set(key, sampleValue(entries.NewValue, field.MapValue(), sentinel, pick, depth))
		case field.Kind() == protoreflect.MessageKind:
			populate(message.Mutable(field).Message(), pick, depth-1)
		default:
			message.Set(field, sampleValue(nil, field, sentinel, pick, depth))
		}
	}
}

func sampleValue(newValue func() protoreflect.Value, field protoreflect.FieldDescriptor, sentinel string, pick oneofPicker,
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
		populate(value.Message(), pick, depth-1)
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
	return populated(t, name, byVariant(variant), 6)
}

func populated(t *testing.T, name protoreflect.FullName, pick oneofPicker, depth int) proto.Message {
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(name)
	require.NoError(t, err)
	message := messageType.New()
	populate(message, pick, depth)
	return message.Interface()
}

// encodedRoots are the messages stored as event payloads; every allow-listed message is reachable from them.
var encodedRoots = []protoreflect.FullName{
	"livekit.AnalyticsEvent", "livekit.Room", "livekit.ParticipantInfo", "livekit.EgressInfo",
	"livekit.IngressInfo", "livekit.SIPCallInfo",
}

func egressRequestVariants() int {
	return (&livekit.EgressInfo{}).ProtoReflect().Descriptor().Oneofs().ByName("request").Fields().Len()
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
	for _, name := range encodedRoots {
		for variant := 0; variant < egressRequestVariants(); variant++ {
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

func TestEveryAllowListedTextFieldSurvives(t *testing.T) {
	expected := map[string]bool{}
	for name, rules := range allowList {
		descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(name)
		require.NoError(t, err)
		fields := descriptor.(protoreflect.MessageDescriptor).Fields()
		for fieldName, rule := range rules {
			// Redacted URLs survive rewritten, so their sentinel does not.
			if rule != redactURL && rule != stripURLCredentials && carriesText(fields.ByName(fieldName)) {
				expected[fmt.Sprintf("%s%s.%s", sentinelPrefix, name, fieldName)] = true
			}
		}
	}

	// Independent oneof choices reach member combinations that a shared position cannot; the unified egress request
	// nests its media sources deeper than the default depth.
	found := map[string]bool{}
	for _, name := range encodedRoots {
		for seed := range uint64(300) {
			message := populated(t, name, randomly(seed), 9)
			for sentinel := range survivors(t, Encode(message, EncodeOptions{StoreMetadata: true})) {
				found[sentinel] = true
			}
		}
	}
	require.Equal(t, expected, found)
}

func carriesText(field protoreflect.FieldDescriptor) bool {
	isText := func(kind protoreflect.Kind) bool {
		return kind == protoreflect.StringKind || kind == protoreflect.BytesKind
	}
	if field.IsMap() {
		return isText(field.MapKey().Kind()) || isText(field.MapValue().Kind())
	}
	return isText(field.Kind())
}

func TestEncodeKeysMatchProtojson(t *testing.T) {
	marshal := protojson.MarshalOptions{UseProtoNames: true}
	for _, name := range encodedRoots {
		for variant := 0; variant < egressRequestVariants(); variant++ {
			for _, opts := range []EncodeOptions{{}, {StoreMetadata: true, AnonymizeIP: true}} {
				message := fullyPopulated(t, name, variant)
				raw, err := marshal.Marshal(message)
				require.NoError(t, err)
				var reference map[string]any
				require.NoError(t, json.Unmarshal(raw, &reference))
				compareDocument(t, string(name), Encode(message, opts), reference, message.ProtoReflect().Descriptor(), opts)
			}
		}
	}
}

// compareDocument checks that a document has the protojson keys of the allow-listed fields, with the set member of a
// oneof nested under the oneof name, and the BSON type of each field kind.
func compareDocument(t *testing.T, path string, document bson.D, reference map[string]any,
	descriptor protoreflect.MessageDescriptor, opts EncodeOptions) {
	rules := allowList[descriptor.FullName()]
	expected := map[string]bool{}
	for key := range reference {
		field := descriptor.Fields().ByName(protoreflect.Name(key))
		require.NotNil(t, field, "%s.%s", path, key)
		if rule, ok := rules[field.Name()]; ok && keepField(rule, opts) && encodable(field) {
			expected[documentKey(field)] = true
		}
	}
	actual := map[string]bool{}
	for _, element := range document {
		actual[element.Key] = true
	}
	require.Equal(t, expected, actual, path)

	for _, element := range document {
		field, value := descriptor.Fields().ByName(protoreflect.Name(element.Key)), element.Value
		if field == nil {
			member := element.Value.(bson.D)
			require.Len(t, member, 1, "%s.%s", path, element.Key)
			field, value = descriptor.Fields().ByName(protoreflect.Name(member[0].Key)), member[0].Value
		}
		fieldPath := path + "." + string(field.Name())
		reference := reference[string(field.Name())]
		switch {
		case field.IsList():
			items, references := value.(bson.A), reference.([]any)
			require.Len(t, items, len(references), fieldPath)
			for i := range items {
				compareValue(t, fieldPath, items[i], references[i], field, opts)
			}
		case field.IsMap():
			entries, references := value.(bson.D), reference.(map[string]any)
			require.Len(t, entries, len(references), fieldPath)
			for _, entry := range entries {
				compareValue(t, fieldPath+"."+entry.Key, entry.Value, references[entry.Key], field.MapValue(), opts)
			}
		default:
			compareValue(t, fieldPath, value, reference, field, opts)
		}
	}
}

func documentKey(field protoreflect.FieldDescriptor) string {
	if oneof := field.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
		return string(oneof.Name())
	}
	return string(field.Name())
}

func compareValue(t *testing.T, path string, value any, reference any, field protoreflect.FieldDescriptor, opts EncodeOptions) {
	switch field.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if field.Message().FullName() == timestampName {
			require.IsType(t, time.Time{}, value, path)
			return
		}
		compareDocument(t, path, value.(bson.D), reference.(map[string]any), field.Message(), opts)
	case protoreflect.EnumKind:
		require.Equal(t, reference, value, path)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed64Kind:
		require.IsType(t, int64(0), value, path)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind, protoreflect.Uint32Kind,
		protoreflect.Fixed32Kind:
		require.IsType(t, int32(0), value, path)
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		require.IsType(t, float64(0), value, path)
	case protoreflect.BoolKind:
		require.IsType(t, true, value, path)
	case protoreflect.StringKind:
		require.IsType(t, "", value, path)
	case protoreflect.BytesKind:
		require.IsType(t, bson.Binary{}, value, path)
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

func TestCustomBaseURLRedacted(t *testing.T) {
	customBaseURL := "https://layouts.example.com/app/index.html?token=abc"
	roomComposite := Encode(&livekit.EgressInfo{
		Request: &livekit.EgressInfo_RoomComposite{RoomComposite: &livekit.RoomCompositeEgressRequest{CustomBaseUrl: customBaseURL}},
	}, EncodeOptions{})
	template := Encode(&livekit.EgressInfo{
		Request: &livekit.EgressInfo_Egress{Egress: &livekit.StartEgressRequest{
			Source: &livekit.StartEgressRequest_Template{Template: &livekit.TemplateSource{CustomBaseUrl: customBaseURL}},
		}},
	}, EncodeOptions{})

	request := lookup(lookup(roomComposite, "request").(bson.D), "room_composite").(bson.D)
	require.Equal(t, "https://layouts.example.com/app/***", lookup(request, "custom_base_url"))
	source := lookup(lookup(lookup(lookup(template, "request").(bson.D), "egress").(bson.D), "source").(bson.D), "template").(bson.D)
	require.Equal(t, "https://layouts.example.com/app/***", lookup(source, "custom_base_url"))
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
