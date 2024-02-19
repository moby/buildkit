package types

type MountType string

const (
	MountTypeBind   MountType = "bind"
	MountTypeCache  MountType = "cache"
	MountTypeTmpfs  MountType = "tmpfs"
	MountTypeSecret MountType = "secret"
	MountTypeSSH    MountType = "ssh"
)

type ShareMode string

const (
	MountSharingShared  ShareMode = "shared"
	MountSharingPrivate ShareMode = "private"
	MountSharingLocked  ShareMode = "locked"
)

type Mount struct {
	Type         MountType
	From         string
	Source       string
	Target       string
	ReadOnly     bool
	SizeLimit    int64
	CacheID      string
	CacheSharing ShareMode
	Required     bool
	Mode         *uint64
	UID          *uint64
	GID          *uint64
}
