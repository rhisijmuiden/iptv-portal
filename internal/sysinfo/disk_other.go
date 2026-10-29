//go:build !linux

package sysinfo

// diskSpace is only measured on Linux, where the portal runs.
func diskSpace(dir string) (total, used, free uint64) { return 0, 0, 0 }
