package unit

import "testing"

func TestSplitName(t *testing.T) {
	cases := []struct {
		name, prefix, instance, typ string
	}{
		{"foo.service", "foo", "", "service"},
		{"getty@tty1.service", "getty", "tty1", "service"},
		{"foo@.service", "foo", "", "service"},
		{"sys-devices-virtual.mount", "sys-devices-virtual", "", "mount"},
		{"noext", "noext", "", ""},
	}
	for _, c := range cases {
		p, i, ty := SplitName(c.name)
		if p != c.prefix || i != c.instance || ty != c.typ {
			t.Errorf("SplitName(%q) = (%q,%q,%q) want (%q,%q,%q)",
				c.name, p, i, ty, c.prefix, c.instance, c.typ)
		}
	}
}

func TestExpandSpecifiers(t *testing.T) {
	sp := Specifiers{
		FullName: "getty@tty1.service", Prefix: "getty", Instance: "tty1",
		Host: "sinty", Home: "/home/user", RuntimeDir: "/run",
	}
	cases := map[string]string{
		"%n":               "getty@tty1.service",
		"%p":               "getty",
		"%i":               "tty1",
		"agetty %I":        "agetty tty1",
		"runs in %t":       "runs in /run",
		"home is %h":       "home is /home/user",
		"host %H":          "host sinty",
		"100%% sure":       "100% sure",
		"keep %z verbatim": "keep %z verbatim",
	}
	for in, want := range cases {
		if got := expandSpecifiers(in, sp); got != want {
			t.Errorf("expand(%q) = %q want %q", in, got, want)
		}
	}
}

func TestUnescapeEscapeName(t *testing.T) {
	if got := UnescapeName("home-user"); got != "home/user" {
		t.Errorf("UnescapeName dash = %q", got)
	}
	if got := UnescapeName("a\\x2db"); got != "a-b" {
		t.Errorf("UnescapeName hex = %q", got)
	}
	if got := EscapeName("/home/user"); got != "-home-user" {
		t.Errorf("EscapeName = %q", got)
	}
	// round-trip for a path
	p := "/var/lib/foo"
	if got := UnescapeName(EscapeName(p)); got != p {
		t.Errorf("round-trip = %q want %q", got, p)
	}
}

func TestResolveSpecifiersInFile(t *testing.T) {
	f := newFile("getty@tty1.service")
	s := f.section("Service")
	s.add("ExecStart", "/sbin/agetty %I")
	f.ResolveSpecifiers(SpecifiersFor(f))
	if v, _ := f.Get("Service", "ExecStart"); v != "/sbin/agetty tty1" {
		t.Errorf("ExecStart = %q", v)
	}
}
