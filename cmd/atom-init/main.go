package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/singularityos-lab/atom/internal/atomctl"
	"github.com/singularityos-lab/atom/internal/pid1"
	"github.com/singularityos-lab/atom/internal/sessionrun"
	"github.com/singularityos-lab/atom/internal/socketact"
)

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
	}

	// Running as PID 1 with no explicit subcommand: this is the boot path.
	if os.Getpid() == 1 && len(argv) < 2 {
		return pid1.Main(argv[1:])
	}

	if len(argv) < 2 {
		fmt.Fprintln(os.Stderr, "atom-init: usage: atom-init <init|ctl|version> [args]")
		return 2
	}

	switch argv[1] {
	case "init":
		return pid1.Main(argv[2:])
	case "ctl":
		return atomctl.Main(argv[2:])
	case "run":
		return sessionrun.Main(argv[2:])
	case "sd-exec":
		return socketact.SdExec(argv[2:])
	case "noop":
		// A no-op that exits 0, used as ExecStart for synthetic boot-test units
		// so the test initramfs needs no external binaries.
		fmt.Printf("atom noop: %v\n", argv[2:])
		return 0
	case "version", "--version", "-v":
		fmt.Printf("atom-init %s\n", version)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "atom-init: unknown subcommand %q\n", argv[1])
		return 2
	}
}
