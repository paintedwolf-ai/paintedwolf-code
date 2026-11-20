package metadatacall

import "golang.org/x/sys/unix"

func addLegacy(all map[uint32]entry) {
	all[unix.SYS_CHMOD] = entry{mode, path}
	all[unix.SYS_CHOWN] = entry{owner, path}
	all[unix.SYS_LCHOWN] = entry{owner, link}
	all[unix.SYS_UTIME] = entry{times, path}
	all[unix.SYS_UTIMES] = entry{times, path}
	all[unix.SYS_FUTIMESAT] = entry{times, at}
}
