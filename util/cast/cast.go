package cast

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TimestampOrNil(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}
func TimeOrNil(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

func ToStringSlice[T ~string](from []T) []string {
	var result []string
	for _, x := range from {
		result = append(result, string(x))
	}
	return result
}

func FromStringSlice[T ~string](from []string) []T {
	var result []T
	for _, x := range from {
		result = append(result, T(x))
	}
	return result
}

func FromStringMap[K ~string, V any](from map[string]V) map[K]V {
	result := make(map[K]V)
	for k, v := range from {
		result[K(k)] = v
	}
	return result
}
