package pid1

import "testing"

func TestApiMountsSecondarySet(t *testing.T) {
	byType := map[string]apiMount{}
	for _, m := range apiMounts {
		byType[m.fstype] = m
	}
	// fusectl is what fusermount -u needs; it must be present and optional.
	fc, ok := byType["fusectl"]
	if !ok {
		t.Fatal("fusectl (/sys/fs/fuse/connections) missing from apiMounts")
	}
	if !fc.optional || fc.target != "/sys/fs/fuse/connections" {
		t.Errorf("fusectl should be optional at /sys/fs/fuse/connections, got %+v", fc)
	}
	for _, ft := range []string{"mqueue", "hugetlbfs", "debugfs", "tracefs", "configfs", "securityfs", "pstore", "bpf"} {
		m, ok := byType[ft]
		if !ok {
			t.Errorf("secondary fs %q missing", ft)
			continue
		}
		if !m.optional {
			t.Errorf("%q must be optional (kernel may not provide it)", ft)
		}
	}
	// The core API filesystems must remain NOT optional.
	for _, ft := range []string{"proc", "sysfs", "devtmpfs", "cgroup2"} {
		if byType[ft].optional {
			t.Errorf("core fs %q must not be optional", ft)
		}
	}
}
