package control

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// DefaultSocket is where PID 1 listens.
const DefaultSocket = "/run/atom/control.sock"

// Request is a command from atomctl.
type Request struct {
	Cmd  string `json:"cmd"`
	Unit string `json:"unit,omitempty"`
}

// UnitStatus is the wire form of a unit snapshot.
type UnitStatus struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	State string `json:"state"`
}

// Reply is the manager's response.
type Reply struct {
	OK    bool         `json:"ok"`
	Error string       `json:"error,omitempty"`
	State string       `json:"state,omitempty"`
	Units []UnitStatus `json:"units,omitempty"`
	Lines []string     `json:"lines,omitempty"`
}

const maxFrame = 8 << 20 // 8 MiB guard

// writeFrame writes a 4-byte big-endian length prefix followed by JSON.
func writeFrame(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > maxFrame {
		return fmt.Errorf("frame too large: %d bytes", len(data))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// readFrame reads a length-prefixed JSON frame into v.
func readFrame(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return fmt.Errorf("frame too large: %d bytes", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
