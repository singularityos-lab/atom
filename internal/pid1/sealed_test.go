package pid1

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mirkobrombin/atomloops/sealedlog"

	"github.com/singularityos-lab/atom/internal/service"
)

func sealedPair(t *testing.T) (*sealedRecorder, func() *net.UnixConn) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dict := filepath.Join(dir, "c")
	os.WriteFile(dict, []byte("4 sinit init\n"), 0o644)
	sock := filepath.Join(dir, "s")
	r := newSealedRecorder(func() (*sealedlog.Writer, error) { return sealedlog.OpenAt("sinit", dict, sock) })
	listen := func() *net.UnixConn {
		c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	return r, listen
}

func next(t *testing.T, c *net.UnixConn) (byte, []uint64) {
	t.Helper()
	buf := make([]byte, 256)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	b := buf[:n]
	if b[0] != 4 {
		t.Fatalf("component %d", b[0])
	}
	code, argc := b[1], int(b[2])
	_, k := binary.Uvarint(b[3:])
	b = b[3+k:]
	var args []uint64
	for i := 0; i < argc; i++ {
		v, k := binary.Uvarint(b)
		args = append(args, v)
		b = b[k:]
	}
	return code, args
}

func TestSealedRecorderKeepsEventsUntilTheCollectorIsUp(t *testing.T) {
	r, listen := sealedPair(t)
	r.log("start")
	r.UnitFailed("broken.service", service.ExitInfo{Code: 3})
	if len(r.pending) != 2 {
		t.Fatalf("pending %d", len(r.pending))
	}
	c := listen()
	r.flush()
	if code, _ := next(t, c); code != 1 {
		t.Fatalf("first record code %d", code)
	}
	code, args := next(t, c)
	if code != 33 || args[0] != unitHash("broken.service") || args[1] != 3 || args[2] != 0 {
		t.Fatalf("unit-fail %d %v", code, args)
	}
	r.UnitRestarted("broken.service", 2)
	if code, args := next(t, c); code != 34 || args[1] != 2 {
		t.Fatalf("unit-restart %d %v", code, args)
	}
	if len(r.pending) != 0 {
		t.Fatalf("pending after flush %d", len(r.pending))
	}
}

func TestSealedRecorderQueueIsBounded(t *testing.T) {
	r, _ := sealedPair(t)
	for i := 0; i < sealedQueue+10; i++ {
		r.log("target", uint64(i))
	}
	if len(r.pending) != sealedQueue || r.pending[0].args[0] != 10 {
		t.Fatalf("queue kept %d, oldest %v", len(r.pending), r.pending[0].args)
	}
}

func TestServiceFailureReachesTheSink(t *testing.T) {
	r, listen := sealedPair(t)
	c := listen()
	service.Events = r
	t.Cleanup(func() { service.Events = nil })
	r.UnitFailed("x.service", service.ExitInfo{Signaled: true, Signal: 9})
	if code, args := next(t, c); code != 33 || args[2] != 9 {
		t.Fatalf("signal not kept %d %v", code, args)
	}
}
