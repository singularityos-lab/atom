package service

import (
	"fmt"
	"strings"

	"github.com/singularityos-lab/atom/internal/unit"
)

// Type is the service start-up/readiness model (systemd Type=).
type Type int

const (
	TypeSimple Type = iota
	TypeExec
	TypeOneshot
	TypeForking
	TypeNotify
	TypeDbus
	TypeIdle
)

func (t Type) String() string {
	switch t {
	case TypeSimple:
		return "simple"
	case TypeExec:
		return "exec"
	case TypeOneshot:
		return "oneshot"
	case TypeForking:
		return "forking"
	case TypeNotify:
		return "notify"
	case TypeDbus:
		return "dbus"
	case TypeIdle:
		return "idle"
	default:
		return "simple"
	}
}

func parseType(s string) Type {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "exec":
		return TypeExec
	case "oneshot":
		return TypeOneshot
	case "forking":
		return TypeForking
	case "notify":
		return TypeNotify
	case "dbus":
		return TypeDbus
	case "idle":
		return TypeIdle
	default:
		return TypeSimple
	}
}

// Config is the runtime projection of a [Service] unit.
type Config struct {
	Name string
	Type Type

	ExecStartPre  []ExecCommand
	ExecStart     []ExecCommand
	ExecStartPost []ExecCommand
	ExecStop      []ExecCommand

	Environment     []string
	WorkingDir      string
	RemainAfterExit bool
	NotifyAccess    string
	PIDFile         string

	// RuntimeDir is the base for the per-service notify socket (default
	// /run/atom). Overridable so the runtime is testable in a temp dir.
	RuntimeDir string
}

// ConfigFromFile projects a parsed unit file into a service Config.
func ConfigFromFile(f *unit.File) (Config, error) {
	c := Config{Name: f.Name, RuntimeDir: "/run/atom"}

	parseList := func(key string) ([]ExecCommand, error) {
		var out []ExecCommand
		for _, line := range f.List("Service", key) {
			ec, err := ParseExecCommand(line)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out = append(out, ec)
		}
		return out, nil
	}

	var err error
	if c.ExecStartPre, err = parseList("ExecStartPre"); err != nil {
		return c, err
	}
	if c.ExecStart, err = parseList("ExecStart"); err != nil {
		return c, err
	}
	if c.ExecStartPost, err = parseList("ExecStartPost"); err != nil {
		return c, err
	}
	if c.ExecStop, err = parseList("ExecStop"); err != nil {
		return c, err
	}

	typeStr, _ := f.Get("Service", "Type")
	c.Type = parseType(typeStr)

	for _, line := range f.List("Service", "Environment") {
		c.Environment = append(c.Environment, strings.Fields(line)...)
	}
	c.WorkingDir = f.GetDefault("Service", "WorkingDirectory", "")
	c.RemainAfterExit = f.Bool("Service", "RemainAfterExit", false)
	c.NotifyAccess = f.GetDefault("Service", "NotifyAccess", "main")
	c.PIDFile = f.GetDefault("Service", "PIDFile", "")
	return c, nil
}
