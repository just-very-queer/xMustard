package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"xmustard/api-go/internal/daemon"
	"xmustard/api-go/internal/workspaceops"
)

// Daemon lifecycle (WS-58): what the API does when a service manager runs it
// (`xmustard-ops setup`). A plain `xmustard-api` run is unchanged unless these apply:
//   - XMUSTARD_LOG_FILE logs to a size-capped, rotated file (XMUSTARD_LOG_MAX_BYTES,
//     default 10 MiB; XMUSTARD_LOG_KEEP generations, default 3), and stdout and stderr
//     follow it so a crash's trace is rotated too.
//   - LISTEN_PID/LISTEN_FDS (systemd socket activation) hand the API its listening
//     socket; the startup interlock judges that socket's address like a bind.
//   - Once the listener is up, the governance store is opened once, which migrates it
//     to this build's schema, and checked with quick_check; /api/health reports it.

// prepareDaemon runs before the server is configured.
func prepareDaemon() error {
	// The human approver's token belongs to the human's terminal (WS-57). The API never
	// reads it, and the agents and helpers it starts must not inherit it.
	_ = os.Unsetenv("XMUSTARD_APPROVER_TOKEN")
	path, maxBytes, keep, err := daemon.LogConfig(os.Getenv)
	if err != nil || path == "" {
		return err
	}
	l, err := daemon.OpenLog(path, maxBytes, keep, true)
	if err != nil {
		return fmt.Errorf("XMUSTARD_LOG_FILE: %w", err)
	}
	log.SetOutput(l)
	return nil
}

// adoptActivatedSocket takes the socket systemd passed, if any, and points cfg at its
// address, so validateStartup judges the address actually served (a non-loopback
// socket needs the same auth and TLS as a non-loopback bind).
func adoptActivatedSocket(cfg *serverConfig) (net.Listener, error) {
	ln, err := daemon.Activated()
	if ln == nil || err != nil {
		return nil, err
	}
	addr := ln.Addr().(*net.TCPAddr) // Activated accepts TCP only
	cfg.host, cfg.port = addr.IP.String(), strconv.Itoa(addr.Port)
	cfg.posture.Loopback = cfg.loopback()
	log.Printf("listener: socket activation passed %s", addr)
	return ln, nil
}

// listen returns the activated socket, or binds cfg's address.
func listen(cfg serverConfig, activated net.Listener) (net.Listener, error) {
	if activated != nil {
		return activated, nil
	}
	return net.Listen("tcp", cfg.addr())
}

// checkStoreAtStart runs the governance store's startup check and logs the outcome.
func checkStoreAtStart(ctx context.Context) {
	h := workspaceops.CheckMemoryStore(ctx, dataDir())
	switch h.Status {
	case workspaceops.StoreOK:
		log.Printf("store: governance store ok (schema v%d; migrated and quick_check passed in %d ms)", h.SchemaVersion, h.CheckMS)
	case workspaceops.StoreNone:
		log.Printf("store: no governance store yet; the first memory write creates it")
	default:
		log.Printf("store: governance store %s: %s; memory calls fail until a backup is restored (xmustard-ops store restore)",
			strings.ToUpper(h.Status), h.Detail)
	}
}
