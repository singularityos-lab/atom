//go:build !linux

package logkeep

import "errors"

// Config names the sources and the destination.
type Config struct{}

// Default is the configuration of the system service.
func Default() Config { return Config{} }

// Run is only implemented on Linux.
func Run(Config) error { return errors.New("logkeep: unsupported platform") }
