package core

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/unit"
)

func TestAcceptSocketRunsOneInstancePerConnection(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	addr := fmt.Sprintf("@atom-accept-%d", os.Getpid())
	write(t, filepath.Join(dir, "t.target"), "[Unit]\n")
	write(t, filepath.Join(dir, "h.socket"), "[Socket]\nAccept=yes\nListenStream="+addr+"\n")
	write(t, filepath.Join(dir, "h@.service"), "[Service]\nType=oneshot\n"+
		"ExecStart=/bin/sh -c 'read x; echo \"%i $x\"'\n"+
		"StandardInput=socket\nStandardOutput=socket\n")
	write(t, filepath.Join(dir, "t.target.wants", "h.socket"), "")

	m, err := Build(&unit.Loader{Paths: []string{dir}}, "t.target")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartTarget(context.Background(), "t.target"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, word := range []string{"one", "two"} {
		c, err := net.DialTimeout("unix", "\x00"+addr[1:], 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprintln(c, word)
		line, err := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if err != nil {
			t.Fatalf("%s: %v", word, err)
		}
		var inst, got string
		fmt.Sscan(line, &inst, &got)
		if got != word {
			t.Fatalf("reply %q, want instance and %q", line, word)
		}
		seen[inst] = true
	}
	if len(seen) != 2 {
		t.Fatalf("instances %v, want two distinct", seen)
	}
}
