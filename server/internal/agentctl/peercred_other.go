//go:build !linux

package agentctl

import "net"

type peerCred struct {
	Known bool
	PID   int32
	UID   uint32
	GID   uint32
}

func readPeerCred(conn net.Conn) peerCred {
	return peerCred{}
}
