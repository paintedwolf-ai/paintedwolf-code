//go:build darwin && cgo

package osprocess

/*
#include <libproc.h>
#include <sys/proc_info.h>
#include <sys/stat.h>
#include <sys/sysctl.h>
#include <errno.h>

static int pw_directory_matches(int pid, int fd, int *matched) {
 struct proc_vnodepathinfo paths;
 struct stat expected;
 if (fstat(fd, &expected)) return errno;
 if (proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &paths, sizeof(paths)) != sizeof(paths)) return errno ? errno : EIO;
 struct vinfo_stat *cwd = &paths.pvi_cdir.vip_vi.vi_stat;
 *matched = cwd->vst_dev == (uint32_t)expected.st_dev && cwd->vst_ino == expected.st_ino;
 return 0;
}
static int pw_process_credentials(int pid, unsigned int result[6]) {
 struct proc_bsdinfo process;
 if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &process, sizeof(process)) != sizeof(process)) return errno ? errno : EIO;
 result[0] = process.pbi_ruid; result[1] = process.pbi_uid; result[2] = process.pbi_svuid;
 result[3] = process.pbi_rgid; result[4] = process.pbi_gid; result[5] = process.pbi_svgid;
 return 0;
}
static int pw_process_arguments(int pid, void *data, size_t *length) {
 int mib[3] = {CTL_KERN, KERN_PROCARGS2, pid};
 return sysctl(mib, 3, data, length, NULL, 0) ? errno : 0;
}
*/
import "C"
