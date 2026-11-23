//go:build linux

package agentctl

import (
	"net"
	"syscall"
)

type peerCred struct {
	Known bool
	PID   int32
	UID   uint32
	GID   uint32
}

func readPeerCred(conn net.Conn) peerCred {
	uc, ok := conn.(*net.UnixConn)
	if !ok || uc == nil {
		return peerCred{}
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return peerCred{}
	}
	var out peerCred
	_ = raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err != nil || cred == nil {
			return
		}
		out = peerCred{
			Known: true,
			PID:   cred.Pid,
			UID:   cred.Uid,
			GID:   cred.Gid,
		}
	})
	return out
}
