package sysinfo

import "syscall"

// diskSpace returns the size, the space in use and the space free for the
// portal of the file system that holds dir, like df.
func diskSpace(dir string) (total, used, free uint64) {
	var st syscall.Statfs_t
	if dir == "" || syscall.Statfs(dir, &st) != nil {
		return 0, 0, 0
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, (st.Blocks - st.Bfree) * bs, st.Bavail * bs
}
