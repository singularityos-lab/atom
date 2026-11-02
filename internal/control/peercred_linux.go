package control

import "syscall"

type peerCred struct {
	Pid int32
	Uid uint32
	Gid uint32
}

func getPeerCred(fd int) (*peerCred, error) {
	c, err := syscall.GetsockoptUcred(fd, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil {
		return nil, err
	}
	return &peerCred{Pid: c.Pid, Uid: c.Uid, Gid: c.Gid}, nil
}
