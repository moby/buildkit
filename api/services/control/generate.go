package moby_buildkit_v1 //nolint:revive

//go:generate protoc -I=. -I=../../../vendor/ -I=../../../../../../ --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative:. control.proto
