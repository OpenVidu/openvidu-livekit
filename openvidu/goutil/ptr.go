package goutil

import "encoding/json"

// Returns a pointer to the given value.
func Ptr[T any](v T) *T {
	return &v
}

// Returns a pointer to a cloned value.
func Clone[T any](v T) (*T, error) {
	dataJson, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var data T
	err = json.Unmarshal(dataJson, &data)
	return &data, err
}
