//go:build darwin && cgo

package workspaceops

/*
#include <libproc.h>
#include <stdlib.h>
#include <string.h>
#include <sys/proc_info.h>

// xm_holds_path reports whether pid has a vnode descriptor whose path is path.
static int xm_holds_path(int pid, const char *path) {
	int size = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
	if (size <= 0) {
		return 0;
	}
	struct proc_fdinfo *fds = malloc(size);
	if (fds == NULL) {
		return 0;
	}
	size = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, fds, size);
	int found = 0;
	for (int i = 0; size > 0 && i < size / (int)PROC_PIDLISTFD_SIZE && !found; i++) {
		if (fds[i].proc_fdtype != PROX_FDTYPE_VNODE) {
			continue;
		}
		struct vnode_fdinfowithpath info;
		if (proc_pidfdinfo(pid, fds[i].proc_fd, PROC_PIDFDVNODEPATHINFO, &info, sizeof info) == (int)sizeof info &&
			strncmp(info.pvip.vip_path, path, sizeof info.pvip.vip_path) == 0) {
			found = 1;
		}
	}
	free(fds);
	return found;
}

// xm_cwd copies pid's working directory into out; it returns 0 on success.
static int xm_cwd(int pid, char *out, size_t cap) {
	struct proc_vnodepathinfo info;
	if (proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &info, sizeof info) != (int)sizeof info) {
		return -1;
	}
	strlcpy(out, info.pvi_cdir.vip_path, cap);
	return 0;
}
*/
import "C"

import "unsafe"

// terminalTTYHolders returns the live processes with a descriptor on ttyPath,
// found with proc_pidinfo(3). Other users' processes cannot be inspected, and
// could not be signalled anyway. Once the session leader has exited, darwin has
// revoked the terminal, and its former holders no longer show its path.
func terminalTTYHolders(ttyPath string, procs []terminalProc) map[int]bool {
	path := C.CString(ttyPath)
	defer C.free(unsafe.Pointer(path))
	holders := make(map[int]bool)
	for _, proc := range procs {
		if !proc.exited && proc.pid > 1 && C.xm_holds_path(C.int(proc.pid), path) == 1 {
			holders[proc.pid] = true
		}
	}
	return holders
}

// terminalProcCwd returns pid's working directory. The teardown tests use it to
// find leftovers without the sweep; it lives here because it needs cgo, which test
// files cannot use.
func terminalProcCwd(pid int) (string, bool) {
	var out [C.MAXPATHLEN]C.char
	if C.xm_cwd(C.int(pid), &out[0], C.size_t(len(out))) != 0 {
		return "", false
	}
	return C.GoString(&out[0]), true
}
