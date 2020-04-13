// +build !windows

package mpatch

import "syscall"

var writeAccess = syscall.PROT_READ | syscall.PROT_WRITE | syscall.PROT_EXEC
var readAccess = syscall.PROT_READ | syscall.PROT_EXEC

func callMProtect(addr uintptr, length int, prot int) error {
	for p := getPageStartPtr(addr); p < addr+uintptr(length); p += uintptr(pageSize) {
		page := getMemorySliceFromPointer(p, pageSize)
		err := syscall.Mprotect(page, prot)
		if err != nil {
			return err
		}
	}
	return nil
}

func copyDataToPtr(ptr uintptr, data []byte) error {
	dataLength := len(data)
	ptrByteSlice := getMemorySliceFromPointer(ptr, len(data))
	err := callMProtect(ptr, dataLength, writeAccess)
	if err != nil {
		return err
	}
	copy(ptrByteSlice, data[:])
	err = callMProtect(ptr, dataLength, readAccess)
	if err != nil {
		return err
	}
	return nil
}
