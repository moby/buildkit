package moby_buildkit_v1_frontend

//go:generate protoc -I=. -I=../../../vendor/ --gogo_out=plugins=grpc:. gateway.proto
