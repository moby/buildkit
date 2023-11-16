package contenthash

//go:generate protoc -I=. -I=../../vendor/ --go_out=paths=source_relative:. checksum.proto

const (
	CacheRecordTypeFile      = CacheRecordType_FILE
	CacheRecordTypeDir       = CacheRecordType_DIR
	CacheRecordTypeDirHeader = CacheRecordType_DIR_HEADER
	CacheRecordTypeSymlink   = CacheRecordType_SYMLINK
)
