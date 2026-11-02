package pid1

import "os"

type apiMount struct {
	source, target, fstype string
	flags                  uintptr
	data                   string
	mode                   os.FileMode
	optional               bool // a kernel fs that may be absent; mount best-effort, quiet if unsupported
}
