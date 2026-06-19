package logd

import (
	"fmt"
	"strings"
	"testing"
)

func TestRingLineSplitAndCap(t *testing.T) {
	r := NewRing(3)
	r.Write([]byte("one\ntwo\n"))
	r.Write([]byte("three\nfour\n")) // exceeds cap of 3
	got := r.Lines()
	if strings.Join(got, ",") != "two,three,four" {
		t.Errorf("lines = %v, want [two three four]", got)
	}
}

func TestRingPartialLine(t *testing.T) {
	r := NewRing(10)
	r.Write([]byte("incomplete"))
	if got := r.Lines(); len(got) != 1 || got[0] != "incomplete" {
		t.Errorf("partial = %v", got)
	}
	r.Write([]byte(" rest\n"))
	if got := r.Lines(); len(got) != 1 || got[0] != "incomplete rest" {
		t.Errorf("completed = %v", got)
	}
}

func TestRegistryPerUnit(t *testing.T) {
	reg := NewRegistry(100)
	fmt.Fprintln(reg.Writer("a.service"), "from a")
	fmt.Fprintln(reg.Writer("b.service"), "from b")
	if got := reg.Lines("a.service"); len(got) != 1 || got[0] != "from a" {
		t.Errorf("a lines = %v", got)
	}
	if got := reg.Lines("b.service"); len(got) != 1 || got[0] != "from b" {
		t.Errorf("b lines = %v", got)
	}
	if got := reg.Lines("missing.service"); got != nil {
		t.Errorf("missing unit should have nil lines, got %v", got)
	}
}
