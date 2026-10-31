//go:build darwin && cgo

package lineage

/*
#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/socketvar.h>
#include <sys/sysctl.h>
#include <netinet/in.h>
#include <netinet/in_pcb.h>
#include <netinet/tcp.h>
#include <netinet/tcp_fsm.h>
#include <netinet/tcp_var.h>
#include <arpa/inet.h>

struct pw_listen_row {
 uint64_t handle;
 unsigned char local[16];
 uint32_t uid;
 uint16_t port;
 unsigned char ipv4;
};

// pw_listen_rows reads the host-wide PCB table, which needs no access to the
// holding process, so a socket whose holder cannot be inspected still appears.
// TCP rows listen in TCPS_LISTEN; UDP rows listen while bound and unconnected.
// xso_so is the handle the shared descriptor walk reports as soi_so.
static int pw_listen_rows(int udp, struct pw_listen_row **rows, int *count) {
 const char *name = udp ? "net.inet.udp.pcblist64" : "net.inet.tcp.pcblist64";
 *rows = NULL;
 *count = 0;
 char *buf = NULL;
 size_t len = 0;
 for (int attempt = 0; attempt < 4; attempt++) {
  len = 0;
  if (sysctlbyname(name, NULL, &len, NULL, 0) != 0) return errno ? errno : EIO;
  len += len / 4 + 4096;
  buf = malloc(len);
  if (buf == NULL) return ENOMEM;
  if (sysctlbyname(name, buf, &len, NULL, 0) == 0) break;
  int err = errno;
  free(buf);
  buf = NULL;
  if (err != ENOMEM) return err ? err : EIO;
 }
 if (buf == NULL) return EAGAIN;
 if (len < sizeof(struct xinpgen)) { free(buf); return EIO; }
 size_t capacity = len / sizeof(struct xinpcb64) + 1;
 struct pw_listen_row *out = calloc(capacity, sizeof(struct pw_listen_row));
 if (out == NULL) { free(buf); return ENOMEM; }
 char *end = buf + len;
 struct xinpgen *gen = (struct xinpgen *)buf;
 for (char *at = buf + gen->xig_len; at + sizeof(struct xinpgen) <= end; ) {
  struct xinpgen *record = (struct xinpgen *)at;
  if (record->xig_len <= sizeof(struct xinpgen) || at + record->xig_len > end) break;
  at += record->xig_len;
  struct xinpcb64 *inp;
  int listening;
  if (udp) {
   if (record->xig_len < sizeof(struct xinpcb64)) continue;
   inp = (struct xinpcb64 *)record;
   listening = inp->inp_lport != 0 && inp->inp_fport == 0;
  } else {
   if (record->xig_len < sizeof(struct xtcpcb64)) continue;
   struct xtcpcb64 *tcp = (struct xtcpcb64 *)record;
   inp = &tcp->xt_inpcb;
   listening = tcp->t_state == TCPS_LISTEN;
  }
  if (!listening || *count >= (int)capacity) continue;
  struct pw_listen_row *row = &out[(*count)++];
  row->handle = inp->xi_socket.xso_so;
  row->uid = inp->xi_socket.so_uid;
  row->port = ntohs(inp->inp_lport);
  row->ipv4 = (inp->inp_vflag & INP_IPV6) == 0;
  if (row->ipv4) {
   memcpy(row->local, &inp->inp_dependladdr.inp46_local.ia46_addr4, 4);
  } else {
   memcpy(row->local, &inp->inp_dependladdr.inp6_local, 16);
  }
 }
 free(buf);
 *rows = out;
 return 0;
}

*/
import "C"

import (
	"net/netip"
	"syscall"
	"unsafe"
)

func listeners(query ListenerQuery) ([]Listener, error) {
	var rows []kernelListener
	for _, protocol := range query.protocols() {
		table, err := pcbListeners(protocol)
		if err != nil {
			return nil, err
		}
		for _, row := range table {
			if query.wants(row.Protocol, row.Local) {
				rows = append(rows, row)
			}
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	pids, err := livePIDs()
	if err != nil {
		return nil, err
	}
	return resolveListeners(rows, pids, observeSockets), nil
}

func pcbListeners(protocol Protocol) ([]kernelListener, error) {
	udp := C.int(0)
	if protocol == ProtocolUDP {
		udp = 1
	}
	var rows *C.struct_pw_listen_row
	var count C.int
	if code := C.pw_listen_rows(udp, &rows, &count); code != 0 {
		return nil, syscall.Errno(code)
	}
	defer C.free(unsafe.Pointer(rows))
	out := make([]kernelListener, 0, int(count))
	for _, row := range unsafe.Slice(rows, int(count)) {
		raw := unsafe.Slice((*byte)(unsafe.Pointer(&row.local[0])), 16)
		var addr netip.Addr
		if row.ipv4 != 0 {
			addr = netip.AddrFrom4([4]byte(raw[:4]))
		} else {
			addr = netip.AddrFrom16([16]byte(raw))
		}
		out = append(out, kernelListener{Protocol: protocol, Local: netip.AddrPortFrom(addr, uint16(row.port)), UID: uint32(row.uid), Handle: uint64(row.handle)})
	}
	return out, nil
}
