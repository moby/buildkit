package winapi

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func decodeEntry(buffer []byte) (string, error) {
	name := make([]uint16, len(buffer)/2)
	err := binary.Read(bytes.NewReader(buffer), binary.LittleEndian, &name)
	if err != nil {
		return "", fmt.Errorf("decoding name: %w", err)
	}
	return string(utf16.Decode(name)), nil
}

type samValue struct {
	Name string
	Data []byte
}

type SAMUser struct {
	Username  string
	SIDString string
	RID       int64
}

func reverse(s []byte) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func (s *SAMUser) SID() (*windows.SID, error) {
	if s.SIDString == "" {
		return nil, fmt.Errorf("no sid available")
	}
	utfPtr, err := syscall.UTF16PtrFromString(s.SIDString)
	if err != nil {
		return nil, fmt.Errorf("converting string to utf-16 ptr: %w", err)
	}
	var sid *windows.SID
	if err := windows.ConvertStringSidToSid(utfPtr, &sid); err != nil {
		return nil, fmt.Errorf("fetching SID: %w", err)
	}
	return sid, nil
}

func getValues(key syscall.Handle) ([]samValue, error) {
	var subKeys uint32
	var nValues uint32
	if err := ORQueryInfoKey(key, nil, nil, &subKeys, nil, nil, &nValues, nil, nil, nil, 0); err != nil {
		return nil, err
	}

	ret := make([]samValue, nValues)

	var j uint32 = 0
	for j = 0; j < nValues; j++ {
		nSize := MAX_VALUE_NAME
		var dwType uint32 = 0
		var cbData uint32 = 0
		valueName := make([]uint16, nSize)
		if err := OREnumValue(key, j, &valueName[0], &nSize, &dwType, nil, &cbData); err != syscall.ERROR_MORE_DATA {
			continue
		}
		buffer := make([]byte, cbData)

		if err := OREnumValue(key, j, &valueName[0], &nSize, &dwType, &buffer[0], &cbData); err != nil {
			return nil, err
		}
		valueNameString := syscall.UTF16ToString(valueName[:nSize])
		ret[j] = samValue{
			Name: valueNameString,
			Data: buffer,
		}
	}
	return ret, nil
}

func parseUserInfo(data samValue, rid int64) (SAMUser, error) {
	usernameOffset := binary.LittleEndian.Uint32(data.Data[12:16])
	usernameLen := binary.LittleEndian.Uint32(data.Data[16:20])
	ret, err := decodeEntry(data.Data[usernameOffset+0xCC : (usernameOffset+0xCC)+usernameLen])
	if err != nil {
		return SAMUser{}, fmt.Errorf("decoding username: %w", err)
	}
	// Before the username offset, we have the Administrators group SID, twice.
	// The user full SID starts 16 bytes before the two Administrator group SIDs.
	// Skip 32 bytes, then fetch preceding 16 bytes we care about.
	// See https://web.archive.org/web/20190423074618/http://www.beginningtoseethelight.org:80/ntsecurity/index.htm
	// Sadly, the archived link is the best description of the format of the SAM hive
	// I have been able to find.
	sidBytes := data.Data[(usernameOffset+0xcc)-48 : (usernameOffset+0xcc)-32]
	first := sidBytes[0:4]
	second := sidBytes[4:8]
	third := sidBytes[8:12]
	fourth := sidBytes[12:16]
	// fmt.Println(first, second, third, fourth)
	reverse(first)
	reverse(second)
	reverse(third)
	reverse(fourth)
	foundRid := binary.BigEndian.Uint32(fourth)
	if foundRid != uint32(rid) {
		return SAMUser{}, nil
	}
	sid := fmt.Sprintf("S-1-5-21-%d-%d-%d-%d", binary.BigEndian.Uint32(first), binary.BigEndian.Uint32(second), binary.BigEndian.Uint32(third), binary.BigEndian.Uint32(fourth))
	return SAMUser{
		Username:  ret,
		SIDString: sid,
		RID:       rid,
	}, nil
}

func walkSAMUsers(rootKey syscall.Handle) ([]SAMUser, error) {
	subkeyName, err := syscall.UTF16PtrFromString("sam\\Domains\\Account\\Users")
	if err != nil {
		return nil, fmt.Errorf("fetching utf16 pointer: %w", err)
	}

	var users syscall.Handle
	if err := OROpenKey(rootKey, subkeyName, &users); err != nil {
		return nil, fmt.Errorf("opening users key: %w", err)
	}
	defer ORCloseKey(&users)

	var subkeys uint32
	var nvalues uint32
	if err := ORQueryInfoKey(users, nil, nil, &subkeys, nil, nil, &nvalues, nil, nil, nil, 0); err != nil {
		return nil, fmt.Errorf("querying key info: %w", err)
	}

	SAMUsers := []SAMUser{}

	var key uint32
	for key = 0; key < subkeys; key++ {
		nSize := MAX_KEY_NAME
		name := make([]uint16, nSize)
		if err := OREnumKey(users, key, &name[0], &nSize, nil, nil, 0); err != nil {
			return nil, fmt.Errorf("enumerating key: %w", err)
		}
		keyName := syscall.UTF16ToString(name[:nSize])
		if strings.EqualFold(keyName, "names") {
			continue
		}

		rid, err := strconv.ParseInt(keyName, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing RID: %w", err)
		}

		data := name[:nSize]
		var userKey syscall.Handle
		if err := OROpenKey(users, &data[0], &userKey); err != nil {
			return nil, fmt.Errorf("opening key: %w", err)
		}

		values, err := getValues(userKey)
		if err != nil {
			return nil, fmt.Errorf("fetching values for key: %w", err)
		}

		for _, val := range values {
			if val.Name == "V" {
				parsed, err := parseUserInfo(val, rid)
				if err != nil {
					return nil, fmt.Errorf("parsing user data: %w", err)
				}
				if parsed.SIDString == "" {
					continue
				}
				SAMUsers = append(SAMUsers, parsed)
			}
		}

	}

	return SAMUsers, nil
}

func GetUserInfoFromOfflineSAMHive(samPath string) ([]SAMUser, error) {
	hivePath, err := syscall.UTF16PtrFromString("C:\\Users\\Administrator\\work\\getuser\\sam2")
	if err != nil {
		return nil, fmt.Errorf("getting path: %v", err)
	}

	var key syscall.Handle
	if err := OROpenHive(hivePath, &key); err != nil {
		return nil, fmt.Errorf("opening hive: %v", err)
	}
	defer ORCloseHive(&key)

	users, err := walkSAMUsers(key)
	if err != nil {
		return nil, fmt.Errorf("parsing hive: %v", err)
	}
	return users, nil
}
