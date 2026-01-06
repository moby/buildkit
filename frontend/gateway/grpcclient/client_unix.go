//go:build !windows

package grpcclient

import "google.golang.org/grpc"

func nPipeDialer() (string, grpc.DialOption) {
	return "", nil
}
