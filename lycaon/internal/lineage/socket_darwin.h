#include <libproc.h>
#include <sys/proc_info.h>
#include <arpa/inet.h>
#include <errno.h>
#include <limits.h>
#include <stddef.h>
#include <stdlib.h>
#include <string.h>

struct pw_tcp_row {
 uint64_t socket_handle, pcb_handle, generation;
 unsigned char local[16], remote[16];
 uint16_t local_port, remote_port;
 unsigned char ipv4, listening;
};

// One native descriptor walk serves listener and exact connected-peer queries.
// The caller checks process incarnation around this snapshot. Kernel observation
// failure is returned, never interpreted as an empty set of foreign holders.
static int pw_tcp_rows(int pid, struct pw_tcp_row **rows, int *count) {
 *rows = NULL;
 *count = 0;
 errno = 0;
 int size = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
 if (size < 0) return errno ? errno : ESRCH;
 if (size == 0) return errno;
 if (size > INT_MAX - 64 * (int)sizeof(struct proc_fdinfo)) return EOVERFLOW;
 size += 64 * (int)sizeof(struct proc_fdinfo);
 struct proc_fdinfo *fds = malloc((size_t)size);
 if (fds == NULL) return ENOMEM;
 int n = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, fds, size);
 if (n < 0 || n >= size) {
  int err = errno ? errno : EAGAIN;
  free(fds);
  return err;
 }
 int fdcount = n / (int)sizeof(struct proc_fdinfo);
 if (fdcount == 0) { free(fds); return 0; }
 struct pw_tcp_row *out = calloc((size_t)fdcount, sizeof(struct pw_tcp_row));
 if (out == NULL) { free(fds); return ENOMEM; }
 int error = 0;
 for (int i = 0; i < fdcount; i++) {
  if (fds[i].proc_fdtype != PROX_FDTYPE_SOCKET) continue;
  struct socket_fdinfo info;
  errno = 0;
  int got = proc_pidfdinfo(pid, fds[i].proc_fd, PROC_PIDFDSOCKETINFO, &info, sizeof(info));
  if (got <= 0) {
   if (errno != EBADF && errno != ENOENT && errno != ESRCH) error = errno ? errno : EIO;
   continue;
  }
  if (got < (int)(offsetof(struct socket_fdinfo, psi.soi_kind) + sizeof(info.psi.soi_kind))) {
   error = EIO; continue;
  }
  if (info.psi.soi_kind != SOCKINFO_TCP) continue;
  if (got < (int)(offsetof(struct socket_fdinfo, psi.soi_proto.pri_tcp) + sizeof(struct tcp_sockinfo))) {
   error = EIO;
   continue;
  }
  if (info.psi.soi_family != AF_INET && info.psi.soi_family != AF_INET6) continue;
  struct in_sockinfo *in = &info.psi.soi_proto.pri_tcp.tcpsi_ini;
  struct pw_tcp_row *row = &out[(*count)++];
  row->socket_handle = info.psi.soi_so;
  row->pcb_handle = info.psi.soi_pcb;
  row->generation = in->insi_gencnt;
  row->local_port = ntohs((uint16_t)in->insi_lport);
  row->remote_port = ntohs((uint16_t)in->insi_fport);
  row->listening = info.psi.soi_proto.pri_tcp.tcpsi_state == TSI_S_LISTEN;
  row->ipv4 = info.psi.soi_family == AF_INET;
  if (row->ipv4) {
   memcpy(row->local, &in->insi_laddr.ina_46.i46a_addr4, 4);
   memcpy(row->remote, &in->insi_faddr.ina_46.i46a_addr4, 4);
  } else {
   memcpy(row->local, &in->insi_laddr.ina_6, 16);
   memcpy(row->remote, &in->insi_faddr.ina_6, 16);
  }
 }
 free(fds);
 if (error) { free(out); *count = 0; return error; }
 *rows = out;
 return 0;
}
