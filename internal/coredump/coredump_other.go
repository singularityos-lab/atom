//go:build !linux

package coredump

import (
	"errors"
	"io"
)

// Config bounds what is kept.
type Config struct{}

// Default is the system configuration.
func Default() Config { return Config{} }

// Run is only implemented on Linux.
func Run(Config, []string, io.Reader) error {
	return errors.New("coredump: unsupported platform")
}
