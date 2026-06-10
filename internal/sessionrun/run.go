package sessionrun

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/singularityos-lab/atom/internal/core"
	"github.com/singularityos-lab/atom/internal/unit"
)

// Main runs the session-manager smoke test and returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	unitDir := fs.String("unit-dir", "", "comma-separated unit dirs, in descending precedence")
	target := fs.String("target", "multi-user.target", "target to start")
	stay := fs.Bool("stay", false, "block after the target is reached")
	withDefaults := fs.Bool("with-default-paths", false, "also search the default atom/systemd paths")
	planOnly := fs.Bool("plan-only", false, "compute and print the transaction without starting anything")
	check := fs.Bool("check", false, "parse every unit file in the dirs, report warnings, start nothing")
	dryRun := fs.Bool("dry-run", false, "simulate service start (no real exec); validates ordering/readiness safely")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var paths []string
	for _, p := range strings.Split(*unitDir, ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if *withDefaults || len(paths) == 0 {
		paths = append(paths, unit.DefaultSearchPath...)
	}

	if *check {
		return checkCorpus(paths)
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

	if *dryRun {
		m.SetDryRun(true)
		fmt.Println("(dry-run: simulating starts, no process is executed)")
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

// checkCorpus parses every unit file across the given dirs and reports totals
// plus any file that produced parse warnings (the "unit mishandled" signal).
func checkCorpus(dirs []string) int {
	exts := map[string]bool{
		".service": true, ".target": true, ".socket": true, ".timer": true,
		".path": true, ".mount": true, ".slice": true, ".scope": true,
		".automount": true, ".swap": true,
	}
	var parsed, warned int
	for _, dir := range dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !exts[filepath.Ext(e.Name())] {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if _, err := os.Stat(full); err != nil {
				// A symlink whose target is absent from this drop is a corpus
				// artifact (e.g. an alias to a unit not copied), not a parser fault.
				fmt.Printf("dangling symlink (skipped): %s\n", e.Name())
				continue
			}
			f, err := unit.ParseFile(full)
			if err != nil {
				fmt.Printf("HARD ERROR %s: %v\n", e.Name(), err)
				warned++
				continue
			}
			parsed++
			if len(f.Warnings) > 0 {
				warned++
				fmt.Printf("WARN %s:\n", e.Name())
				for _, w := range f.Warnings {
					fmt.Printf("    %s\n", w)
				}
			}
		}
	}
	fmt.Printf("checked %d unit files, %d with warnings/errors\n", parsed, warned)
	if warned > 0 {
		return 1
	}
	return 0
}
