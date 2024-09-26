package moby_buildkit_v1_sourcepolicy //nolint:revive

//go:generate protoc -I=. --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative,require_unimplemented_servers=false:. policy.proto
