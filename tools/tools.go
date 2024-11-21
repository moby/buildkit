//go:build tools

// Package tools tracks dependencies on binaries not referenced in this codebase.
// https://github.com/golang/go/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module
// Disclaimer: Avoid adding tools that don't need to be inferred from go.mod
// like golangci-lint and check they don't import too many dependencies.
package tools

import (
	_ "github.com/planetscale/vtprotobuf/cmd/protoc-gen-go-vtproto"
	_ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
	_ "google.golang.org/protobuf/cmd/protoc-gen-go"
)
