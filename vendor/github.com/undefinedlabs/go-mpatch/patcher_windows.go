// +build windows

package mpatch

import (
	"syscall"
	"unsafe"
)

const pageExecuteReadAndWrite = 0x40

var virtualProtectProc = syscall.NewLazyDLL("kernel32.dll").NewProc("VirtualProtect")

func callVirtualProtect(lpAddress uintptr, dwSize int, flNewProtect uint32, lpflOldProtect unsafe.Pointer) error {
	ret, _, _ := virtualProtectProc.Call(lpAddress, uintptr(dwSize), uintptr(flNewProtect), uintptr(lpflOldProtect))
	if ret == 0 {
		return syscall.GetLastError()
	}
	return nil
}

func copyDataToPtr(ptr uintptr, data []byte) error {
	var oldPerms, tmp uint32
	dataLength := len(data)
	ptrByteSlice := getMemorySliceFromPointer(ptr, len(data))
	err := callVirtualProtect(ptr, dataLength, pageExecuteReadAndWrite, unsafe.Pointer(&oldPerms))
	if err != nil {
		return err
	}
	copy(ptrByteSlice, data[:])
	err = callVirtualProtect(ptr, dataLength, oldPerms, unsafe.Pointer(&tmp))
	if err != nil {
		return err
	}
	return nil
}
