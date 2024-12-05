// Copyright 2024 OpenVidu
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

package goutil

import (
	"errors"
	"fmt"
	"reflect"
)

// Reads an unexported (private) struct field.
//
// Input: a pointer to a struct, and the name of the field to read.
//
// Output: a pointer to the field value, or an error.
//
// Example:
//
//	type Example struct {
//		Public  int
//		private int
//	}
//	e := Example{Public: 1, private: 2}
//	p, err := goutil.GetPrivateStructField[int](&e, "private")
//	if err == nil {
//		fmt.Println(*p) // 2
//	}
func GetPrivateStructField[F any, S any](s *S, field string) (*F, error) {
	rv := reflect.ValueOf(s)

	// Unwrap pointers and interfaces to get the underlying value.
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		//fmt.Printf("unwrapping value type %q\n", rv.Kind())
		rv = rv.Elem()
	}

	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("invalid value type %q", rv.Kind())
	}
	rv = rv.FieldByName(field)

	if !rv.CanAddr() {
		return nil, errors.New("value is not addressable")
	}
	rv = reflect.NewAt(rv.Type(), rv.Addr().UnsafePointer()).Elem()

	if !rv.CanInterface() {
		return nil, errors.New("underlying field value cannot be accessed")
	}
	value, ok := rv.Interface().(F)

	if !ok {
		return nil, errors.New("underlying field value is not the correct type")
	}

	return &value, nil
}

// Writes an unexported (private) struct field.
// func setUnexportedFieldValue(field reflect.Value, value interface{}) {
//     reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).
//         Elem().
//         Set(reflect.ValueOf(value))
// }
