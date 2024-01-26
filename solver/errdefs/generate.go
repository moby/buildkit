package errdefs

//go:generate protoc -I=. -I=../../vendor/ -I=../../../../../ --go_out=paths=source_relative:. errdefs.proto
