package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/singularityos-lab/atom/internal/atomctl"
	"github.com/singularityos-lab/atom/internal/pid1"
	"github.com/singularityos-lab/atom/internal/sessionrun"
	"github.com/singularityos-lab/atom/internal/socketact"
	"github.com/singularityos-lab/atom/internal/syslogd"
)

// runSyslogd runs the minimal system log daemon (journald replacement), as a
// service under the init: sinit syslogd [--socket /dev/log] [--output FILE].
func runSyslogd(args []string) int {
	fs := flag.NewFlagSet("syslogd", flag.ContinueOnError)
	sock := fs.String("socket", "/dev/log", "datagram socket to serve")
	out := fs.String("output", "/var/log/messages", "log file to append to")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s, err := syslogd.New(syslogd.Config{SocketPath: *sock, OutputPath: *out})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sinit syslogd:", err)
		return 1
	}
	defer s.Close()
	if err := s.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "sinit syslogd:", err)
		return 1
	}
	return 0
}

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args))
}

func run(argv []string) int {
	base := filepath.Base(argv[0])

	// Multicall dispatch by program name (symlinks).
	switch base {
	case "atomctl":
		return atomctl.Main(argv[1:])
	case "init":
		return pid1.Main(argv[1:])
	case "reboot", "poweroff", "halt":
		// classic power commands: /usr/bin/{reboot,poweroff,halt} symlinked to this
		// binary map to the matching atomctl verb, so the shell/DE power path works.
		return atomctl.Main([]string{base})
	}

	// Running as PID 1 with no explicit subcommand: this is the boot path.
	if os.Getpid() == 1 && len(argv) < 2 {
		return pid1.Main(argv[1:])
	}

	if len(argv) < 2 {
		fmt.Fprintln(os.Stderr, "sinit: usage: sinit <init|ctl|version> [args]")
		return 2
	}

	switch argv[1] {
	case "init":
		return pid1.Main(argv[2:])
	case "ctl":
		return atomctl.Main(argv[2:])
	case "run":
		return sessionrun.Main(argv[2:])
	case "syslogd":
		return runSyslogd(argv[2:])
	case "sd-exec":
		return socketact.SdExec(argv[2:])
	case "noop":
		// A no-op that exits 0, used as ExecStart for synthetic boot-test units
		// so the test initramfs needs no external binaries.
		fmt.Printf("atom noop: %v\n", argv[2:])
		return 0
	case "version", "--version", "-v":
		fmt.Printf("sinit %s\n", version)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "sinit: unknown subcommand %q\n", argv[1])
		return 2
	}
}
