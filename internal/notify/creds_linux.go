package notify

import "syscall"

const scmCredentials = syscall.SCM_CREDENTIALS

func enableCredentials(fd int) {
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1)
}

func parseCredentials(m *syscall.SocketControlMessage) (int, uint32, uint32, bool) {
	cred, err := syscall.ParseUnixCredentials(m)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(cred.Pid), cred.Uid, cred.Gid, true
}
