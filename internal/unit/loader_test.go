package unit

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoaderFragmentAndDropin(t *testing.T) {
	hi := t.TempDir() // higher precedence
	lo := t.TempDir() // lower precedence
	l := &Loader{Paths: []string{hi, lo}}

	// fragment in the low-precedence dir
	writeFile(t, filepath.Join(lo, "demo.service"), `[Service]
Type=simple
ExecStart=/bin/original
`)
	// drop-in in the high-precedence dir overrides Type
	writeFile(t, filepath.Join(hi, "demo.service.d", "10-override.conf"), `[Service]
Type=notify
`)

	f, err := l.Load("demo.service")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Get("Service", "Type"); v != "notify" {
		t.Errorf("Type after drop-in = %q, want notify", v)
	}
	if v, _ := f.Get("Service", "ExecStart"); v != "/bin/original" {
		t.Errorf("ExecStart = %q", v)
	}
}

func TestLoaderHigherPrecedenceFragmentWins(t *testing.T) {
	hi := t.TempDir()
	lo := t.TempDir()
	l := &Loader{Paths: []string{hi, lo}}
	writeFile(t, filepath.Join(lo, "x.service"), "[Service]\nExecStart=/bin/low\n")
	writeFile(t, filepath.Join(hi, "x.service"), "[Service]\nExecStart=/bin/high\n")
	f, err := l.Load("x.service")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Get("Service", "ExecStart"); v != "/bin/high" {
		t.Errorf("ExecStart = %q, want /bin/high", v)
	}
}

func TestLoaderTemplateInstance(t *testing.T) {
	dir := t.TempDir()
	l := &Loader{Paths: []string{dir}}
	writeFile(t, filepath.Join(dir, "getty@.service"), `[Service]
ExecStart=/sbin/agetty %I
`)
	f, err := l.Load("getty@tty1.service")
	if err != nil {
		t.Fatal(err)
	}
	if f.Instance != "tty1" {
		t.Errorf("Instance = %q, want tty1", f.Instance)
	}
	if v, _ := f.Get("Service", "ExecStart"); v != "/sbin/agetty tty1" {
		t.Errorf("ExecStart = %q, want specifier-expanded", v)
	}
}

func TestLoaderTemplateDropinThenInstance(t *testing.T) {
	dir := t.TempDir()
	l := &Loader{Paths: []string{dir}}
	writeFile(t, filepath.Join(dir, "worker@.service"), "[Service]\nExecStart=/bin/worker\nType=simple\n")
	// template-wide drop-in
	writeFile(t, filepath.Join(dir, "worker@.service.d", "10-all.conf"), "[Service]\nType=exec\n")
	// instance-specific drop-in overrides the template-wide one
	writeFile(t, filepath.Join(dir, "worker@special.service.d", "20-special.conf"), "[Service]\nType=notify\n")

	f, err := l.Load("worker@special.service")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Get("Service", "Type"); v != "notify" {
		t.Errorf("Type = %q, want notify (instance drop-in wins)", v)
	}
}
