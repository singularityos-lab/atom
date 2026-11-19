package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/unit"
)

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvironmentFileReachesTheService(t *testing.T) {
	requireBins(t, "/bin/sh")
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env")
	if err := os.WriteFile(envPath, []byte("# levels\nA=1\n\nB=\"two words\"\nC = '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	script := writeScript(t, dir, "show", "echo \"$A|$B|$C|$D\" > "+out+"\n")
	s := New(Config{
		Name: "env.service", Type: TypeOneshot,
		Environment:      []string{"A=0", "D=4"},
		EnvironmentFiles: []string{envPath + " -" + filepath.Join(dir, "missing")},
		ExecStart:        []ExecCommand{cmd(t, script)},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "1|two words|3|4" {
		t.Errorf("service saw %q, want %q", strings.TrimSpace(string(got)), "1|two words|3|4")
	}
}

func TestMissingRequiredEnvironmentFileFailsTheStart(t *testing.T) {
	requireBins(t, "/bin/true")
	s := New(Config{
		Name: "env.service", Type: TypeOneshot,
		EnvironmentFiles: []string{filepath.Join(t.TempDir(), "missing")},
		ExecStart:        []ExecCommand{cmd(t, "/bin/true")},
	})
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded without its EnvironmentFile")
	}
}

func TestExecStopPostRunsOnStop(t *testing.T) {
	requireBins(t, "/bin/sleep", "/bin/touch")
	marker := filepath.Join(t.TempDir(), "stopped")
	s := New(Config{
		Name: "post.service", Type: TypeSimple,
		ExecStart:    []ExecCommand{cmd(t, "/bin/sleep 30")},
		ExecStopPost: []ExecCommand{cmd(t, "/bin/touch "+marker)},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("ExecStopPost did not run on stop: %v", err)
	}
}

func TestExecStopPostRunsWhenTheServiceDies(t *testing.T) {
	requireBins(t, "/bin/sh", "/bin/touch")
	dir := t.TempDir()
	marker := filepath.Join(dir, "died")
	script := writeScript(t, dir, "die", "sleep 0.2\nexit 3\n")
	s := New(Config{
		Name: "post.service", Type: TypeSimple,
		ExecStart:    []ExecCommand{cmd(t, script)},
		ExecStopPost: []ExecCommand{cmd(t, "/bin/touch "+marker)},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.State() != Failed && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s.State() != Failed {
		t.Fatalf("state = %s, want failed", s.State())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("ExecStopPost did not run after the service died: %v", err)
	}
}

func TestConfigReadsStopPostAndEnvironmentFiles(t *testing.T) {
	src := `[Service]
ExecStart=/usr/bin/watch
ExecStopPost=/usr/bin/mute --reason gone
EnvironmentFile=-/etc/demo.conf
`
	f, err := unit.Parse("demo.service", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	c, err := ConfigFromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ExecStopPost) != 1 || c.ExecStopPost[0].Path() != "/usr/bin/mute" {
		t.Errorf("ExecStopPost = %+v", c.ExecStopPost)
	}
	if len(c.EnvironmentFiles) != 1 || c.EnvironmentFiles[0] != "-/etc/demo.conf" {
		t.Errorf("EnvironmentFiles = %v", c.EnvironmentFiles)
	}
}
