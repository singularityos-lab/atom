package control

import (
	"syscall"
	"unsafe"
)

type peerCred struct {
	Pid int32
	Uid uint32
	Gid uint32
}

type xucred struct {
	Version uint32
	Uid     uint32
	Ngroups int16
	_       int16
	Groups  [16]uint32
	_       uint32
	Pid     int64
}

const (
	solLocal      = 0
	localPeercred = 1
)

func getPeerCred(fd int) (*peerCred, error) {
	var x xucred
	size := uint32(unsafe.Sizeof(x))
	if _, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(fd), solLocal, localPeercred,
		uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&size)), 0); errno != 0 {
		return nil, errno
	}
	c := &peerCred{Pid: int32(x.Pid), Uid: x.Uid}
	if x.Ngroups > 0 {
		c.Gid = x.Groups[0]
	}
	return c, nil
}
