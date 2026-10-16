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

package migration

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/protojson"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Legacy documents are encoding/json round-trips of the protocol structs stored through a map: snake_case proto
// field names, every number a double, zero values absent, some enums rewritten to their names, oneof wrappers under
// their Go names ("Request": {"RoomComposite": {...}}) and timestamps as {seconds, nanos}. Normalizing them against
// the message descriptor turns them into protojson, which accepts enums by name or number.

// LegacyIDPrefix prefixes the raw event id of a legacy event that has no LiveKit id.
const LegacyIDPrefix = "AE_LEG_"

// Event is a decoded legacy event.
type Event struct {
	Message *livekit.AnalyticsEvent
	// LegacyID is the _id of the legacy document: an md5 hex string, or the hex of the ObjectId of a fixer event.
	LegacyID string
	// Fake marks a close event the legacy active-entities fixer inserted (ObjectId _id and no LiveKit id): it is not
	// an authentic event and its entity is closed by the reconciler instead.
	Fake bool
	// Seconds is the timestamp second the replay orders by.
	Seconds int64
}

// SkipError is a legacy document that cannot be replayed.
type SkipError struct {
	ID     string
	Reason string
}

func (e *SkipError) Error() string {
	return fmt.Sprintf("legacy document %s skipped: %s", e.ID, e.Reason)
}

var (
	eventDescriptor = (&livekit.AnalyticsEvent{}).ProtoReflect().Descriptor()
	statDescriptor  = (&livekit.AnalyticsStat{}).ProtoReflect().Descriptor()
)

// DecodeEvent decodes a legacy `events` document.
func DecodeEvent(raw bson.Raw) (Event, error) {
	document, id, fake, err := prepare(raw)
	if err != nil {
		return Event{}, err
	}
	if reason := unknownType(raw); reason != "" {
		return Event{}, &SkipError{ID: id, Reason: reason}
	}
	event := &livekit.AnalyticsEvent{}
	legacyID := stringField(document, "id")
	if err := decode(document, eventDescriptor, event); err != nil {
		return Event{}, &SkipError{ID: id, Reason: err.Error()}
	}
	if event.GetType() == livekit.AnalyticsEventType_ROOM_CREATED && event.GetRoomId() == "" {
		event.RoomId = event.GetRoom().GetSid()
	}
	switch {
	case legacyID != "":
		event.Id = legacyID
	case !fake:
		event.Id = LegacyIDPrefix + id
	}
	return Event{Message: event, LegacyID: id, Fake: fake && legacyID == "", Seconds: secondsOf(raw, "timestamp")}, nil
}

// DecodeStat decodes a legacy `stats` document.
func DecodeStat(raw bson.Raw) (*livekit.AnalyticsStat, error) {
	document, id, _, err := prepare(raw)
	if err != nil {
		return nil, err
	}
	stat := &livekit.AnalyticsStat{}
	statID := stringField(document, "id")
	if err := decode(document, statDescriptor, stat); err != nil {
		return nil, &SkipError{ID: id, Reason: err.Error()}
	}
	stat.Id = statID
	return stat, nil
}

// Seconds returns the timestamp second of a legacy event (`timestamp`) or stat (`time_stamp`) document.
func Seconds(raw bson.Raw, field string) int64 {
	return secondsOf(raw, field)
}

// prepare unmarshals a legacy document and reads its _id: a string, or an ObjectId for fixer events.
func prepare(raw bson.Raw) (bson.D, string, bool, error) {
	var document bson.D
	if err := bson.Unmarshal(raw, &document); err != nil {
		return nil, "", false, &SkipError{ID: "?", Reason: err.Error()}
	}
	switch value := raw.Lookup("_id"); value.Type {
	case bson.TypeObjectID:
		return document, value.ObjectID().Hex(), true, nil
	case bson.TypeString:
		return document, value.StringValue(), false, nil
	default:
		return nil, "", false, &SkipError{ID: value.String(), Reason: "unexpected _id type " + value.Type.String()}
	}
}

// unknownType reports a missing or unknown event type, which protojson would otherwise decode as the zero value.
func unknownType(raw bson.Raw) string {
	value, err := raw.LookupErr("type")
	if err != nil {
		return "no type"
	}
	values := eventDescriptor.Fields().ByName("type").Enum().Values()
	if name, ok := value.StringValueOK(); ok {
		if values.ByName(protoreflect.Name(name)) == nil {
			return "unknown type " + name
		}
		return ""
	}
	if n, ok := number(value); ok && values.ByNumber(protoreflect.EnumNumber(n)) != nil {
		return ""
	}
	return "unknown type " + value.String()
}

func stringField(document bson.D, key string) string {
	for _, element := range document {
		if element.Key == key {
			value, _ := element.Value.(string)
			return value
		}
	}
	return ""
}

func secondsOf(raw bson.Raw, field string) int64 {
	value, err := raw.LookupErr(field, "seconds")
	if err != nil {
		return 0
	}
	if n, ok := number(value); ok {
		return int64(n)
	}
	return 0
}

func number(value bson.RawValue) (float64, bool) {
	switch value.Type {
	case bson.TypeDouble:
		return value.Double(), true
	case bson.TypeInt32:
		return float64(value.Int32()), true
	case bson.TypeInt64:
		return float64(value.Int64()), true
	}
	return 0, false
}

// decode normalizes a legacy document into protojson and unmarshals it into message.
func decode(document bson.D, descriptor protoreflect.MessageDescriptor, message proto.Message) error {
	normalized := normalizeMessage(document, descriptor, true)
	data, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	return protojson.Unmarshal(data, message)
}

// ignoredKeys are added by the legacy writer or restored by the decoder after unmarshalling.
var ignoredKeys = map[string]bool{"_id": true, "openvidu_expire_at": true, "id": true}

func normalizeMessage(document bson.D, descriptor protoreflect.MessageDescriptor, root bool) map[string]any {
	out := map[string]any{}
	for _, element := range document {
		if root && ignoredKeys[element.Key] {
			continue
		}
		if field := descriptor.Fields().ByName(protoreflect.Name(element.Key)); field != nil {
			if value, ok := normalizeField(element.Value, field); ok {
				out[string(field.Name())] = value
			}
			continue
		}
		oneof := oneofByGoName(descriptor, element.Key)
		wrapper, isDocument := element.Value.(bson.D)
		if oneof == nil || !isDocument || len(wrapper) != 1 {
			continue
		}
		member := memberByGoName(oneof, wrapper[0].Key)
		if member == nil {
			continue
		}
		if value, ok := normalizeField(wrapper[0].Value, member); ok {
			out[string(member.Name())] = value
		}
	}
	return out
}

func oneofByGoName(descriptor protoreflect.MessageDescriptor, goName string) protoreflect.OneofDescriptor {
	oneofs := descriptor.Oneofs()
	for i := 0; i < oneofs.Len(); i++ {
		if oneof := oneofs.Get(i); !oneof.IsSynthetic() && goCase(string(oneof.Name())) == goName {
			return oneof
		}
	}
	return nil
}

func memberByGoName(oneof protoreflect.OneofDescriptor, goName string) protoreflect.FieldDescriptor {
	fields := oneof.Fields()
	for i := 0; i < fields.Len(); i++ {
		if field := fields.Get(i); goCase(string(field.Name())) == goName {
			return field
		}
	}
	return nil
}

// goCase converts a proto name to the Go name protoc-gen-go derives from it: room_composite becomes RoomComposite.
func goCase(name string) string {
	var builder strings.Builder
	upper := true
	for _, r := range name {
		switch {
		case r == '_':
			upper = true
		case upper && r >= 'a' && r <= 'z':
			builder.WriteRune(r - 'a' + 'A')
			upper = false
		default:
			builder.WriteRune(r)
			upper = r >= '0' && r <= '9'
		}
	}
	return builder.String()
}

func normalizeField(value any, field protoreflect.FieldDescriptor) (any, bool) {
	if value == nil {
		return nil, false
	}
	switch {
	case field.IsList():
		items, ok := value.(bson.A)
		if !ok {
			return nil, false
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			if normalized, ok := normalizeSingular(item, field); ok {
				out = append(out, normalized)
			}
		}
		return out, true
	case field.IsMap():
		entries, ok := value.(bson.D)
		if !ok {
			return nil, false
		}
		out := map[string]any{}
		for _, entry := range entries {
			if normalized, ok := normalizeSingular(entry.Value, field.MapValue()); ok {
				out[entry.Key] = normalized
			}
		}
		return out, true
	default:
		return normalizeSingular(value, field)
	}
}

func normalizeSingular(value any, field protoreflect.FieldDescriptor) (any, bool) {
	if value == nil {
		return nil, false
	}
	switch field.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		document, ok := value.(bson.D)
		if !ok {
			return nil, false
		}
		return normalizeWellKnown(document, field.Message())
	case protoreflect.EnumKind:
		if name, ok := value.(string); ok {
			return name, true
		}
		return integer(value)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind, protoreflect.Int64Kind,
		protoreflect.Sint64Kind, protoreflect.Sfixed64Kind, protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return integer(value)
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		f, ok := toFloat(value)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, false
		}
		return f, true
	default:
		return value, true
	}
}

// normalizeWellKnown renders the well-known types in their protojson form; other messages are normalized field by
// field.
func normalizeWellKnown(document bson.D, descriptor protoreflect.MessageDescriptor) (any, bool) {
	switch descriptor.FullName() {
	case "google.protobuf.Timestamp":
		seconds, nanos := secondsAndNanos(document)
		return time.Unix(seconds, nanos).UTC().Format(time.RFC3339Nano), true
	case "google.protobuf.Duration":
		seconds, nanos := secondsAndNanos(document)
		return strconv.FormatFloat(float64(seconds)+float64(nanos)/1e9, 'f', -1, 64) + "s", true
	case "google.protobuf.BoolValue", "google.protobuf.StringValue", "google.protobuf.Int32Value",
		"google.protobuf.Int64Value", "google.protobuf.UInt32Value", "google.protobuf.UInt64Value",
		"google.protobuf.FloatValue", "google.protobuf.DoubleValue", "google.protobuf.BytesValue":
		for _, element := range document {
			if element.Key == "value" {
				return normalizeSingular(element.Value, descriptor.Fields().ByName("value"))
			}
		}
		return nil, false
	default:
		return normalizeMessage(document, descriptor, false), true
	}
}

func secondsAndNanos(document bson.D) (int64, int64) {
	var seconds, nanos int64
	for _, element := range document {
		f, ok := toFloat(element.Value)
		if !ok {
			continue
		}
		switch element.Key {
		case "seconds":
			seconds = int64(f)
		case "nanos":
			nanos = int64(f)
		}
	}
	return seconds, nanos
}

// integer renders a legacy number as an integer JSON number; doubles above 2^53 keep the precision the legacy writer
// stored (under 1 ms for nanosecond timestamps).
func integer(value any) (any, bool) {
	switch v := value.(type) {
	case int32:
		return json.Number(strconv.FormatInt(int64(v), 10)), true
	case int64:
		return json.Number(strconv.FormatInt(v, 10)), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
		return json.Number(strconv.FormatFloat(math.Trunc(v), 'f', -1, 64)), true
	}
	return nil, false
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}
