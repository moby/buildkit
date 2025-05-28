//go:build !windows

package grpcclient

import "google.golang.org/grpc"

func nPipeDialer(_ string) (string, grpc.DialOption) {
	return "", nil
}
