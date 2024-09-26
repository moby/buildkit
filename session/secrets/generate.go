package secrets

//go:generate protoc --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative,require_unimplemented_servers=false:. secrets.proto
