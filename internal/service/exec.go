package service

import (
	"fmt"
	"strings"
)

// ExecCommand is a parsed Exec* directive: the argv plus the systemd modifier
// prefixes that precede the executable.
type ExecCommand struct {
	Argv []string

	IgnoreFailure  bool // '-'  : a non-zero exit is not a failure
	Argv0Override  bool // '@'  : the next token is argv[0]
	FullPrivileges bool // '+'  : run without sandboxing restrictions
	Elevated       bool // '!' / '!!' : elevated privileges for namespace setup
	NoEnvExpand    bool // ':'  : disable environment-variable substitution
}

// Path is the executable to run (the first argv element).
func (c ExecCommand) Path() string {
	if len(c.Argv) == 0 {
		return ""
	}
	return c.Argv[0]
}

// ParseExecCommand parses one Exec* line: leading modifier prefixes in any
// order, then a shell-like argument list with single/double quotes and
// backslash escapes.
func ParseExecCommand(s string) (ExecCommand, error) {
	s = strings.TrimSpace(s)
	var ec ExecCommand
	i := 0
loop:
	for i < len(s) {
		switch s[i] {
		case '-':
			ec.IgnoreFailure = true
		case '@':
			ec.Argv0Override = true
		case '+':
			ec.FullPrivileges = true
		case '!':
			ec.Elevated = true
		case ':':
			ec.NoEnvExpand = true
		default:
			break loop
		}
		i++
	}
	argv, err := splitArgs(strings.TrimSpace(s[i:]))
	if err != nil {
		return ec, err
	}
	if len(argv) == 0 {
		return ec, fmt.Errorf("empty exec command")
	}
	ec.Argv = argv
	return ec, nil
}

func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg := false
	flush := func() {
		if inArg {
			args = append(args, cur.String())
			cur.Reset()
			inArg = false
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			flush()
			i++
		case '\'':
			inArg = true
			i++
			for i < len(s) && s[i] != '\'' {
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated single quote")
			}
			i++
		case '"':
			inArg = true
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					cur.WriteByte(unescapeChar(s[i+1]))
					i += 2
					continue
				}
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated double quote")
			}
			i++
		case '\\':
			inArg = true
			if i+1 < len(s) {
				cur.WriteByte(unescapeChar(s[i+1]))
				i += 2
			} else {
				i++
			}
		default:
			inArg = true
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return args, nil
}

func unescapeChar(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	default:
		return c
	}
}
