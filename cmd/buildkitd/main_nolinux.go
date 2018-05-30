// +build !linux

package main

func runningAsUnprivilegedUser() bool {
	return false
}
