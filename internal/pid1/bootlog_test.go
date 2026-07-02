package pid1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBootLogSinkAndQuiet: logf always records to the tailable sink (buffered
// before it opens, flushed on open), and `quiet` suppresses only the console echo,
// never the sink -- so the debug overlay/serial still see everything.
func TestBootLogSinkAndQuiet(t *testing.T) {
	op, ok, oq, os0, oks, ob := bootLogPath, kmsgPath, logQuiet, logSink, kmsgSink, logBuf
	defer func() { bootLogPath, kmsgPath, logQuiet, logSink, kmsgSink, logBuf = op, ok, oq, os0, oks, ob }()

	bootLogPath = filepath.Join(t.TempDir(), "boot.log")
	kmsgPath = "" // do not write to the real /dev/kmsg in tests
	logSink, kmsgSink, logBuf, logQuiet = nil, nil, nil, true

	logf("early %d", 1) // buffered: sink not open yet
	openLogSink()       // opens the file and flushes the buffer
	logf("later %d", 2) // written straight to the sink

	b, err := os.ReadFile(bootLogPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "atom[1]: early 1") {
		t.Errorf("buffered early line not flushed to sink:\n%s", s)
	}
	if !strings.Contains(s, "atom[1]: later 2") {
		t.Errorf("post-open line not in sink:\n%s", s)
	}
}
