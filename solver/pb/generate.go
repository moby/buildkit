package pb

//go:generate protoc -I=. -I=../../vendor/ -I=../../vendor/github.com/gogo/protobuf/ --go_out=paths=source_relative:. ops.proto
