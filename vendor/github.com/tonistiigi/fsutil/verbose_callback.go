package fsutil

type VerboseProgressStatus int

const (
	StatusStat VerboseProgressStatus = iota
	StatusSkipped
	StatusSending
	StatusSent
	StatusFailed
)

type VerboseProgressCB func(string, VerboseProgressStatus, int)
