package moby_buildkit_v1

//go:generate protoc -I=. -I=../../../vendor/ --gogo_out=plugins=grpc:. control.proto
