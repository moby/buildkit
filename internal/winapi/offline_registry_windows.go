package winapi

const (
	REG_OPTION_CREATE_LINK  uint32 = 0x00000002
	REG_OPTION_NON_VOLATILE uint32 = 0x00000000
	MAX_KEY_NAME            uint32 = 255
	MAX_VALUE_NAME          uint32 = 16383
)

//go:generate go run github.com/Microsoft/go-winio/tools/mkwinsyscall -output zsyscall_windows.go ./*.go
//sys OROpenHive(file *uint16, key *syscall.Handle) (regerrno error) = offreg.OROpenHive
//sys ORCloseHive(key *syscall.Handle) (regerrno error) = offreg.ORCloseHive
//sys OROpenKey(rootKey syscall.Handle, lpSubKeyName *uint16, key *syscall.Handle) (regerrno error) = offreg.OROpenKey
//sys ORCloseKey(key *syscall.Handle) (regerrno error) = offreg.ORCloseKey
//sys OREnumKey(key syscall.Handle, index uint32, name *uint16, nameSize *uint32, class *uint16, classSize *uint32, ftLastWriteTime uintptr) (regerrno error) = offreg.OREnumKey
//sys OREnumValue(key syscall.Handle, index uint32, valueName *uint16, valueNameSize *uint32, valueType *uint32, data *byte, bufferSize *uint32) (regerrno error) = offreg.OREnumValue
//sys ORQueryInfoKey(key syscall.Handle, lpClass *uint16, lpcClass *uint32, lpcSubKeys *uint32, lpcMaxSubKeyLen *uint32, lpcMaxClassLen *uint32, lpcValues *uint32, lpcMaxValueNameLen *uint32, lpcMaxValueLen *uint32, lpcbSecurityDescriptor *uint32, lpftLastWriteTime uintptr) (regerrno error) = offreg.ORQueryInfoKey
