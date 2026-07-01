package unit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMaskedUnit(t *testing.T) {
	dir := t.TempDir()
	// `systemctl mask` == symlink the unit fragment to /dev/null.
	if err := os.Symlink("/dev/null", filepath.Join(dir, "systemd-timesyncd.service")); err != nil {
		t.Fatal(err)
	}
	l := &Loader{Paths: []string{dir}}

	f, err := l.Load("systemd-timesyncd.service")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Masked {
		t.Fatal("a unit symlinked to /dev/null must be Masked")
	}

	// A real fragment right next to it is NOT masked (no false positives).
	os.WriteFile(filepath.Join(dir, "chrony.service"), []byte("[Service]\nExecStart=/usr/sbin/chronyd\n"), 0o644)
	rf, err := l.Load("chrony.service")
	if err != nil {
		t.Fatal(err)
	}
	if rf.Masked {
		t.Error("a real unit must not be Masked")
	}
}
