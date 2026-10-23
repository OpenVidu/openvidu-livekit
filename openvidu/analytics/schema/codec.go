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
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EncodeOptions controls the personal data kept by Encode.
type EncodeOptions struct {
	// StoreMetadata keeps room metadata, participant metadata and attributes and ingress participant metadata.
	StoreMetadata bool
	// AnonymizeIP drops client IP addresses.
	AnonymizeIP bool
}

const redacted = "***"

var timestampName = (&timestamppb.Timestamp{}).ProtoReflect().Descriptor().FullName()

// Encode converts a protocol message into a document holding only allow-listed fields: protobuf field names, enum
// values as names, 64-bit integers as int64, 32-bit integers as int32 (unsigned values above 2^31-1 as int64),
// timestamps as UTC times with millisecond precision, the set member of a oneof as a document named after the oneof,
// and no entry for unset fields. A message missing from the allow-list encodes to nil.
func Encode(message proto.Message, opts EncodeOptions) bson.D {
	if message == nil {
		return nil
	}
	reflected := message.ProtoReflect()
	if !reflected.IsValid() {
		return nil
	}
	return encodeMessage(reflected, opts)
}

func encodeMessage(message protoreflect.Message, opts EncodeOptions) bson.D {
	rules, listed := allowList[message.Descriptor().FullName()]
	if !listed {
		return nil
	}

	document := bson.D{}
	fields := message.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		rule, allowed := rules[field.Name()]
		if !allowed || !message.Has(field) || !keepField(rule, opts) || !encodable(field) {
			continue
		}

		value := encodeField(message.Get(field), field, rule, opts)
		if oneof := field.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
			document = append(document, bson.E{Key: string(oneof.Name()), Value: bson.D{{Key: string(field.Name()), Value: value}}})
		} else {
			document = append(document, bson.E{Key: string(field.Name()), Value: value})
		}
	}
	return document
}

func keepField(rule fieldRule, opts EncodeOptions) bool {
	switch rule {
	case metadata:
		return opts.StoreMetadata
	case clientAddress:
		return !opts.AnonymizeIP
	default:
		return true
	}
}

// encodable reports whether a message-typed field (or map value) has a type the encoder may copy.
func encodable(field protoreflect.FieldDescriptor) bool {
	if field.IsMap() {
		field = field.MapValue()
	}
	if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
		return true
	}
	name := field.Message().FullName()
	_, listed := allowList[name]
	return listed || name == timestampName
}

func encodeField(value protoreflect.Value, field protoreflect.FieldDescriptor, rule fieldRule, opts EncodeOptions) any {
	switch {
	case field.IsList():
		list := value.List()
		array := make(bson.A, 0, list.Len())
		for i := 0; i < list.Len(); i++ {
			array = append(array, encodeSingular(list.Get(i), field, rule, opts))
		}
		return array
	case field.IsMap():
		document := bson.D{}
		value.Map().Range(func(key protoreflect.MapKey, item protoreflect.Value) bool {
			document = append(document, bson.E{Key: key.String(), Value: encodeSingular(item, field.MapValue(), rule, opts)})
			return true
		})
		return document
	default:
		return encodeSingular(value, field, rule, opts)
	}
}

func encodeSingular(value protoreflect.Value, field protoreflect.FieldDescriptor, rule fieldRule, opts EncodeOptions) any {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return value.Bool()
	case protoreflect.EnumKind:
		if enumValue := field.Enum().Values().ByNumber(value.Enum()); enumValue != nil {
			return string(enumValue.Name())
		}
		return int32(value.Enum())
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return int32(value.Int())
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		if value.Uint() > math.MaxInt32 {
			return int64(value.Uint())
		}
		return int32(value.Uint())
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return value.Int()
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return int64(value.Uint())
	case protoreflect.FloatKind:
		// Re-parse at float32 precision so 0.05 is stored as 0.05, not as 0.05000000074505806.
		rounded, _ := strconv.ParseFloat(strconv.FormatFloat(value.Float(), 'g', -1, 32), 64)
		return rounded
	case protoreflect.DoubleKind:
		return value.Float()
	case protoreflect.StringKind:
		switch rule {
		case redactURL:
			return RedactURL(value.String())
		case stripURLCredentials:
			return StripURLCredentials(value.String())
		default:
			return value.String()
		}
	case protoreflect.BytesKind:
		return bson.Binary{Data: value.Bytes()}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		message := value.Message()
		if message.Descriptor().FullName() == timestampName {
			seconds := message.Get(message.Descriptor().Fields().ByName("seconds")).Int()
			nanos := message.Get(message.Descriptor().Fields().ByName("nanos")).Int()
			return time.Unix(seconds, nanos).UTC().Truncate(time.Millisecond)
		}
		return encodeMessage(message, opts)
	}
	return nil
}

// RedactURL removes the userinfo, the query, the fragment and the last path segment of a URL, which may carry a stream
// key or a token: rtmps://user:pw@live.example.com/app/live_123?token=abc becomes rtmps://live.example.com/app/***.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return redacted
	}
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if cut := strings.LastIndex(path, "/"); cut >= 0 && cut < len(path)-1 {
		path = path[:cut+1] + redacted
	}
	return parsed.Scheme + "://" + parsed.Host + path
}

// StripURLCredentials removes the userinfo, the query and the fragment of a URL and keeps its path.
func StripURLCredentials(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return redacted
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
}
