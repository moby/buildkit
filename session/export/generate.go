package export

//go:generate protoc -I=. -I=../../vendor/ --gogoslick_out=plugins=grpc:. export.proto
