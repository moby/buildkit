package moby_buildkit_v1_apicaps //nolint:revive

//go:generate protoc -I=. -I=../../../vendor/ -I=../../../../../../ --go_out=paths=source_relative:. caps.proto
