package sessionrun

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/singularityos-lab/atom/internal/core"
	"github.com/singularityos-lab/atom/internal/unit"
)

// Main runs the session-manager smoke test and returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	unitDir := fs.String("unit-dir", "", "directory of unit files (highest precedence)")
	target := fs.String("target", "multi-user.target", "target to start")
	stay := fs.Bool("stay", false, "block after the target is reached")
	withDefaults := fs.Bool("with-default-paths", false, "also search the default atom/systemd paths")
	planOnly := fs.Bool("plan-only", false, "compute and print the transaction without starting anything")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var paths []string
	if *unitDir != "" {
		paths = append(paths, *unitDir)
	}
	if *withDefaults || *unitDir == "" {
		paths = append(paths, unit.DefaultSearchPath...)
	}
	loader := &unit.Loader{Paths: paths}

	m, err := core.Build(loader, *target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}

	plan := m.Plan(*target)
	fmt.Printf("target %s: %d units, %d layers\n", *target, len(plan.Units), len(plan.Layers))
	if len(plan.Missing) > 0 {
		fmt.Printf("missing requirements: %v\n", plan.Missing)
	}
	if len(plan.BrokenEdges) > 0 {
		fmt.Printf("ordering cycles broken: %v\n", plan.BrokenEdges)
	}
	for i, layer := range plan.Layers {
		fmt.Printf("  layer %d: %v\n", i, layer)
	}

	if *planOnly {
		return 0
	}

	fmt.Printf("starting %s ...\n", *target)
	startErr := m.StartTarget(context.Background(), *target)

	names := append([]string(nil), plan.Units...)
	sort.Strings(names)
	fmt.Println("state:")
	for _, n := range names {
		fmt.Printf("  %-40s %s\n", n, m.State(n))
	}
	if startErr != nil {
		fmt.Fprintln(os.Stderr, "start error:", startErr)
		return 1
	}

	if *stay {
		fmt.Println("target reached; staying up (Ctrl-C to exit)")
		select {}
	}
	return 0
}
