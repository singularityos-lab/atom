package service

import (
	"strings"
	"testing"
)

func TestParseExecSimple(t *testing.T) {
	ec, err := ParseExecCommand("/usr/bin/foo --bar baz")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ec.Argv, "|") != "/usr/bin/foo|--bar|baz" {
		t.Errorf("argv = %v", ec.Argv)
	}
	if ec.IgnoreFailure || ec.Argv0Override {
		t.Errorf("unexpected prefixes on plain command")
	}
}

func TestParseExecPrefixes(t *testing.T) {
	ec, err := ParseExecCommand("@-/bin/x arg1")
	if err != nil {
		t.Fatal(err)
	}
	if !ec.IgnoreFailure || !ec.Argv0Override {
		t.Errorf("prefixes not parsed: %+v", ec)
	}
	if ec.Path() != "/bin/x" {
		t.Errorf("path = %q", ec.Path())
	}
}

func TestParseExecDashIgnoreFailure(t *testing.T) {
	ec, _ := ParseExecCommand("-/usr/bin/maybe-fails")
	if !ec.IgnoreFailure || ec.Path() != "/usr/bin/maybe-fails" {
		t.Errorf("got %+v", ec)
	}
}

func TestParseExecQuotes(t *testing.T) {
	ec, err := ParseExecCommand(`/bin/sh -c "echo hi there"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ec.Argv) != 3 || ec.Argv[2] != "echo hi there" {
		t.Errorf("argv = %v", ec.Argv)
	}

	ec2, _ := ParseExecCommand(`/bin/echo 'single quoted value'`)
	if len(ec2.Argv) != 2 || ec2.Argv[1] != "single quoted value" {
		t.Errorf("argv = %v", ec2.Argv)
	}
}

func TestParseExecUnterminated(t *testing.T) {
	if _, err := ParseExecCommand(`/bin/x "oops`); err == nil {
		t.Error("expected error on unterminated quote")
	}
	if _, err := ParseExecCommand("-"); err == nil {
		t.Error("expected error on prefix-only (empty command)")
	}
}
