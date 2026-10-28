//go:build darwin && cgo

package osprocess

/*
#include <libproc.h>
#include <sys/proc.h>
#include <sys/proc_info.h>
#include <sys/stat.h>
#include <mach/vm_prot.h>
#include <mach-o/loader.h>
#include <mach-o/fat.h>
#include <libkern/OSByteOrder.h>
#include <unistd.h>
#include <errno.h>
#include <stdint.h>

// Only MH_EXECUTE headers at actual selected file-slice offsets identify a
// main image. A mapped dylib/dynamic linker is never an executable pin.
static int pw_exec_offsets(int fd, cpu_type_t cpu, uint64_t offsets[64], int *count) {
 uint32_t magic;
 *count = 0;
 if (pread(fd, &magic, sizeof(magic), 0) != sizeof(magic)) return ENOEXEC;
 if (magic == MH_MAGIC || magic == MH_MAGIC_64) {
  struct mach_header header;
  if (pread(fd, &header, sizeof(header), 0) != sizeof(header)) return ENOEXEC;
  if (header.cputype != cpu || header.filetype != MH_EXECUTE) return ENOEXEC;
  offsets[(*count)++] = 0;
  return 0;
 }
 int wide = magic == FAT_MAGIC_64 || magic == FAT_CIGAM_64;
 int swap = magic == FAT_CIGAM || magic == FAT_CIGAM_64;
 if (!wide && magic != FAT_MAGIC && magic != FAT_CIGAM) return ENOEXEC;
 struct fat_header fat;
 if (pread(fd, &fat, sizeof(fat), 0) != sizeof(fat)) return ENOEXEC;
 uint32_t n = swap ? OSSwapInt32(fat.nfat_arch) : fat.nfat_arch;
 if (n == 0 || n > 64) return EOVERFLOW;
 for (uint32_t i = 0; i < n; i++) {
  uint64_t offset;
  cpu_type_t type;
  if (wide) {
   struct fat_arch_64 arch;
   if (pread(fd, &arch, sizeof(arch), sizeof(fat) + i * sizeof(arch)) != sizeof(arch)) return ENOEXEC;
   type = swap ? OSSwapInt32(arch.cputype) : arch.cputype;
   offset = swap ? OSSwapInt64(arch.offset) : arch.offset;
  } else {
   struct fat_arch arch;
   if (pread(fd, &arch, sizeof(arch), sizeof(fat) + i * sizeof(arch)) != sizeof(arch)) return ENOEXEC;
   type = swap ? OSSwapInt32(arch.cputype) : arch.cputype;
   offset = swap ? OSSwapInt32(arch.offset) : arch.offset;
  }
  if (type != cpu) continue;
  if (offset > INT64_MAX) return EOVERFLOW;
  struct mach_header header;
  if (pread(fd, &header, sizeof(header), (off_t)offset) != sizeof(header)) return ENOEXEC;
  if ((header.magic != MH_MAGIC && header.magic != MH_MAGIC_64) || header.cputype != cpu || header.filetype != MH_EXECUTE) return ENOEXEC;
  offsets[(*count)++] = offset;
 }
 return *count ? 0 : ENOEXEC;
}

static int pw_stopped_image(int pid, int fd, int *matched) {
 *matched = 0;
 struct proc_bsdinfo process;
 if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &process, sizeof(process)) != sizeof(process)) return errno ? errno : EIO;
 if (process.pbi_status != SSTOP) return EAGAIN;
 struct stat expected;
 if (fstat(fd, &expected)) return errno;
 struct proc_archinfo architecture;
 if (proc_pidinfo(pid, PROC_PIDARCHINFO, 0, &architecture, sizeof(architecture)) != sizeof(architecture)) return errno ? errno : EIO;
 uint64_t offsets[64]; int offset_count;
 int format_error = pw_exec_offsets(fd, architecture.p_cputype, offsets, &offset_count);
 if (format_error) return format_error;
 uint64_t address = 0;
 for (int i = 0; i < 4096; i++) {
  struct proc_regionwithpathinfo region;
  errno = 0;
  int n = proc_pidinfo(pid, PROC_PIDREGIONPATHINFO, address, &region, sizeof(region));
  if (n <= 0) {
   // EINVAL marks the end of the finite region table, not missing visibility.
   if (errno != EINVAL) return errno ? errno : EIO;
   break;
  }
  if (n != sizeof(region)) return EIO;
  struct vinfo_stat *file = &region.prp_vip.vip_vi.vi_stat;
  if ((region.prp_prinfo.pri_protection & VM_PROT_EXECUTE) &&
      file->vst_dev == (uint32_t)expected.st_dev && file->vst_ino == expected.st_ino) {
   for (int candidate = 0; candidate < offset_count; candidate++) {
    if (region.prp_prinfo.pri_offset == offsets[candidate]) *matched = 1;
   }
  }
  uint64_t next = region.prp_prinfo.pri_address + region.prp_prinfo.pri_size;
  if (next <= address) return EIO;
  address = next;
  if (i == 4095) return EOVERFLOW;
 }
 if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &process, sizeof(process)) != sizeof(process)) return errno ? errno : EIO;
 return process.pbi_status == SSTOP ? 0 : EAGAIN;
}
*/
import "C"

import (
	"fmt"
	"os"
	"syscall"
)

func matchStoppedExecutable(pid int, expected *os.File) (bool, error) {
	var matched C.int
	conn, err := expected.SyscallConn()
	if err != nil {
		return false, err
	}
	var code C.int
	if err := conn.Control(func(fd uintptr) { code = C.pw_stopped_image(C.int(pid), C.int(fd), &matched) }); err != nil {
		return false, err
	}
	if code != 0 {
		return false, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, syscall.Errno(code))
	}
	return matched != 0, nil
}
