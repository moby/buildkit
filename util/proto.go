package util

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

func PointerSlice[T any](from []T) []*T {
	var result []*T
	for _, x := range from {
		x := x
		result = append(result, &x)
	}
	return result
}

func FromPointerSlice[T any](from []*T) []T {
	var result []T
	for _, x := range from {
		result = append(result, *x)
	}
	return result
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

func FromPointerMap[K ~string, V any](from map[string]*V) map[K]V {
	result := make(map[K]V)
	for k, v := range from {
		result[K(k)] = *v
	}
	return result
}

func ToPointerMap[K ~string, V any](from map[K]V) map[string]*V {
	result := make(map[string]*V)
	for k, v := range from {
		v := v
		result[string(k)] = &v
	}
	return result
}
