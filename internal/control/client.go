package control

import (
	"net"
	"time"
)

// Send dials the control socket, sends one request, and returns the reply.
func Send(path string, req Request) (Reply, error) {
	conn, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return Reply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := writeFrame(conn, req); err != nil {
		return Reply{}, err
	}
	var rep Reply
	if err := readFrame(conn, &rep); err != nil {
		return Reply{}, err
	}
	return rep, nil
}
