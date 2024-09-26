package filesync

//go:generate protoc -I=. -I=../../vendor/ --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative,require_unimplemented_servers=false:. filesync.proto
