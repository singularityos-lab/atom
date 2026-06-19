package atomctl

import (
	"flag"
	"fmt"
	"os"

	"github.com/singularityos-lab/atom/internal/control"
)

// Main runs the client and returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("atomctl", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	socket := fs.String("socket", control.DefaultSocket, "control socket path")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	rest := fs.Args()
	if len(rest) == 0 {
		usage()
		return 2
	}
	cmd := rest[0]
	switch cmd {
	case "help", "-h", "--help":
		usage()
		return 0
	}

	var unitName string
	if len(rest) > 1 {
		unitName = rest[1]
	}

	rep, err := control.Send(*socket, control.Request{Cmd: cmd, Unit: unitName})
	if err != nil {
		fmt.Fprintf(os.Stderr, "atomctl: cannot reach atom at %s: %v\n", *socket, err)
		return 1
	}
	if rep.Error != "" {
		fmt.Fprintf(os.Stderr, "atomctl: %s\n", rep.Error)
		return 1
	}

	switch cmd {
	case "list-units":
		for _, u := range rep.Units {
			fmt.Printf("%-40s %-8s %s\n", u.Name, u.Kind, u.State)
		}
	case "status":
		fmt.Println(rep.State)
	case "logs":
		for _, l := range rep.Lines {
			fmt.Println(l)
		}
	default:
		fmt.Println("ok")
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: atomctl [--socket PATH] <command> [unit]")
	fmt.Fprintln(os.Stderr, "commands: list-units, status <unit>, start <unit>, stop <unit>,")
	fmt.Fprintln(os.Stderr, "          restart <unit>, daemon-reload, boot-confirm")
}
