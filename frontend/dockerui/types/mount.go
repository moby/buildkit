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
	Type         MountType `json:"type,omitempty"`
	From         string    `json:"from,omitempty"`
	Source       string    `json:"source,omitempty"`
	Target       string    `json:"target,omitempty"`
	ReadOnly     bool      `json:"readOnly,omitempty"`
	SizeLimit    int64     `json:"sizeLimit,omitempty"`
	CacheID      string    `json:"cacheID,omitempty"`
	CacheSharing ShareMode `json:"cacheSharing,omitempty"`
	Required     bool      `json:"required,omitempty"`
	// Env optionally specifies the name of the environment variable for a secret.
	// A pointer to an empty value uses the default
	Env  *string `json:"env,omitempty"`
	Mode *uint64 `json:"mode,omitempty"`
	UID  *uint64 `json:"uid,omitempty"`
	GID  *uint64 `json:"gid,omitempty"`
}
