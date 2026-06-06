package atomctl

import (
	"flag"
	"fmt"
	"os"
)

// Main runs the client and returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("atomctl", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	rest := fs.Args()
	if len(rest) == 0 {
		usage()
		return 2
	}

	switch rest[0] {
	case "list-units", "status", "start", "stop", "restart", "reload",
		"enable", "disable", "cat", "logs", "isolate", "daemon-reload",
		"boot-confirm":
		fmt.Fprintf(os.Stderr, "atomctl: %q not yet wired to the control socket\n", rest[0])
		return 1
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "atomctl: unknown command %q\n", rest[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: atomctl <command> [args]")
	fmt.Fprintln(os.Stderr, "commands: list-units status start stop restart reload")
	fmt.Fprintln(os.Stderr, "          enable disable cat logs isolate daemon-reload boot-confirm")
}
