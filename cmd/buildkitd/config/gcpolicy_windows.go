//go:build windows
// +build windows

package config

// set as double that for Linux since
// Windows images are generally larger.
var DiskSpacePercentage int64 = 20
