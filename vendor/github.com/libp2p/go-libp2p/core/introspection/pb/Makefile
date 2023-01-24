PB = $(wildcard *.proto)
GO = $(PB:.proto=.pb.go)

all: $(GO)

%.pb.go: %.proto
	protoc --proto_path=$(PWD):$(PWD)/../..:$(GOPATH)/src --gogofaster_out=Mgoogle/protobuf/timestamp.proto=github.com/gogo/protobuf/types:. $<

clean:
	rm -f *.pb.go
	rm -f *.go