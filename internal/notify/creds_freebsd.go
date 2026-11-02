package notify

import (
	"encoding/binary"
	"syscall"
)

const (
	solLocal             = 0
	localCredsPersistent = 3
	scmCredentials       = 0x08
)

func enableCredentials(fd int) {
	_ = syscall.SetsockoptInt(fd, solLocal, localCredsPersistent, 1)
}

func parseCredentials(m *syscall.SocketControlMessage) (int, uint32, uint32, bool) {
	d := m.Data
	if len(d) < 28 {
		return 0, 0, 0, false
	}
	pid := int(int32(binary.LittleEndian.Uint32(d[4:8])))
	uid := binary.LittleEndian.Uint32(d[8:12])
	gid := binary.LittleEndian.Uint32(d[16:20])
	return pid, uid, gid, true
}
