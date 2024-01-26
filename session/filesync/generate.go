package filesync

//go:generate protoc -I=. -I=../../vendor/ -I=../../vendor/github.com/tonistiigi/fsutil/types/ --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative:. filesync.proto
