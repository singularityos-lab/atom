package unit

import (
	"strings"
	"testing"
	"time"
)

func TestParseBasic(t *testing.T) {
	src := `
# a comment
[Unit]
Description=Demo service
After=network.target  basic.target
After=dbus.service

[Service]
Type=notify
ExecStart=/usr/bin/demo --flag
Environment=A=1
Environment=B=2
`
	f, err := Parse("demo.service", strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Type != TypeService {
		t.Errorf("type = %q, want service", f.Type)
	}
	if got, _ := f.Get("Unit", "Description"); got != "Demo service" {
		t.Errorf("Description = %q", got)
	}
	// After= unions across lines and splits on whitespace.
	after := f.Strv("Unit", "After")
	want := []string{"network.target", "basic.target", "dbus.service"}
	if strings.Join(after, ",") != strings.Join(want, ",") {
		t.Errorf("After = %v, want %v", after, want)
	}
	// Environment= keeps each assignment as a whole value (List, not Strv).
	if env := f.List("Service", "Environment"); len(env) != 2 || env[0] != "A=1" || env[1] != "B=2" {
		t.Errorf("Environment = %v", env)
	}
}

func TestListResetSemantics(t *testing.T) {
	src := `[Service]
ExecStartPre=/bin/a
ExecStartPre=/bin/b
ExecStartPre=
ExecStartPre=/bin/c
`
	f, _ := Parse("x.service", strings.NewReader(src))
	got := f.List("Service", "ExecStartPre")
	if len(got) != 1 || got[0] != "/bin/c" {
		t.Errorf("after reset, ExecStartPre = %v, want [/bin/c]", got)
	}
}

func TestLastWinsScalar(t *testing.T) {
	src := `[Service]
Type=simple
Type=notify
`
	f, _ := Parse("x.service", strings.NewReader(src))
	if v, _ := f.Get("Service", "Type"); v != "notify" {
		t.Errorf("Type = %q, want notify", v)
	}
}

func TestLineContinuation(t *testing.T) {
	src := `[Service]
ExecStart=/usr/bin/tool \
    --one \
    --two
`
	f, _ := Parse("x.service", strings.NewReader(src))
	v, _ := f.Get("Service", "ExecStart")
	if !strings.Contains(v, "--one") || !strings.Contains(v, "--two") || !strings.HasPrefix(v, "/usr/bin/tool") {
		t.Errorf("continuation joined wrong: %q", v)
	}
}

func TestMalformedIsTotal(t *testing.T) {
	src := `garbage line
[Unit]
this has no equals
Description=ok
[Broken
Key=val
`
	f, err := Parse("x.service", strings.NewReader(src))
	if err != nil {
		t.Fatalf("parser must not error on bad content: %v", err)
	}
	if v, _ := f.Get("Unit", "Description"); v != "ok" {
		t.Errorf("Description = %q despite surrounding garbage", v)
	}
	if len(f.Warnings) == 0 {
		t.Errorf("expected warnings for malformed lines")
	}
}

func TestRepeatedSectionMerges(t *testing.T) {
	src := `[Unit]
Wants=a.service
[Service]
ExecStart=/bin/true
[Unit]
Wants=b.service
`
	f, _ := Parse("x.service", strings.NewReader(src))
	w := f.Strv("Unit", "Wants")
	if len(w) != 2 || w[0] != "a.service" || w[1] != "b.service" {
		t.Errorf("Wants across repeated [Unit] = %v", w)
	}
}

func TestBoolAndDuration(t *testing.T) {
	src := `[Service]
RemainAfterExit=yes
PrivateTmp=off
TimeoutStopSec=90
WatchdogSec=1min30s
RestartSec=250ms
`
	f, _ := Parse("x.service", strings.NewReader(src))
	if !f.Bool("Service", "RemainAfterExit", false) {
		t.Error("RemainAfterExit should be true")
	}
	if f.Bool("Service", "PrivateTmp", true) {
		t.Error("PrivateTmp should be false")
	}
	if d := f.Duration("Service", "TimeoutStopSec", 0); d != 90*time.Second {
		t.Errorf("TimeoutStopSec = %v", d)
	}
	if d := f.Duration("Service", "WatchdogSec", 0); d != 90*time.Second {
		t.Errorf("WatchdogSec = %v, want 1m30s", d)
	}
	if d := f.Duration("Service", "RestartSec", 0); d != 250*time.Millisecond {
		t.Errorf("RestartSec = %v", d)
	}
}

func TestParseTimeSpan(t *testing.T) {
	cases := map[string]time.Duration{
		"30s":     30 * time.Second,
		"5min":    5 * time.Minute,
		"1h":      time.Hour,
		"2h30min": 2*time.Hour + 30*time.Minute,
		"100ms":   100 * time.Millisecond,
		"42":      42 * time.Second,
		"1d":      24 * time.Hour,
	}
	for in, want := range cases {
		got, ok := ParseTimeSpan(in)
		if !ok || got != want {
			t.Errorf("ParseTimeSpan(%q) = %v,%v want %v", in, got, ok, want)
		}
	}
	if _, ok := ParseTimeSpan("nonsense"); ok {
		t.Error("nonsense should not parse")
	}
}
