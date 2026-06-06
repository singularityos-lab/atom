package pid1

import (
	"fmt"
	"os"
)

// Main runs the init path. It returns a process exit code; as true PID 1 it
// is not expected to return under normal operation.
func Main(args []string) int {
	if os.Getpid() == 1 {
		fmt.Fprintln(os.Stderr, "atom init: PID 1 boot path not yet implemented")
		// As real PID 1, returning would panic the kernel; for now refuse
		// loudly so this can never be shipped as init by accident.
		return 1
	}
	fmt.Fprintln(os.Stderr, "atom init: not running as PID 1; refusing (use the VM harness)")
	return 1
}
