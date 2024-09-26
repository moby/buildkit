package pb

//go:generate protoc -I=. -I=../../vendor/ --go_out=paths=source_relative:. ops.proto
