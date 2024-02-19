package jsonutil

import (
	"bytes"
	"encoding/json"
)

// UnmarshalStrict is similar to [json.Unmarshal] but strict.
func UnmarshalStrict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
