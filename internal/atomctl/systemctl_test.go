package atomctl

import (
	"reflect"
	"testing"
)

func TestTranslateSystemctl(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCtl  []string // expected atomctl argv, nil if short-circuited
		wantExit int      // only checked when wantCtl is nil
	}{
		{"start", []string{"start", "foo"}, []string{"start", "foo"}, 0},
		{"stop with .service", []string{"stop", "foo.service"}, []string{"stop", "foo.service"}, 0},
		{"status no unit", []string{"status"}, []string{"status"}, 0},
		{"is-active maps to status", []string{"is-active", "foo"}, []string{"status", "foo"}, 0},
		{"list-unit-files maps to list-units", []string{"list-unit-files"}, []string{"list-units"}, 0},
		{"reboot", []string{"reboot"}, []string{"reboot"}, 0},
		{"flags are ignored", []string{"--no-pager", "--no-legend", "list-units"}, []string{"list-units"}, 0},
		{"unknown verb passes through", []string{"kill", "foo"}, []string{"kill", "foo"}, 0},
		{"user is a no-op ok", []string{"--user", "start", "x.target"}, nil, 0},
		{"enable is honest failure", []string{"enable", "foo"}, nil, 1},
		{"no verb", nil, nil, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateSystemctl(tt.args)
			if tt.wantCtl != nil {
				if !reflect.DeepEqual(got.atomctlArgs, tt.wantCtl) {
					t.Fatalf("atomctlArgs = %v, want %v", got.atomctlArgs, tt.wantCtl)
				}
				return
			}
			if got.atomctlArgs != nil {
				t.Fatalf("expected short-circuit, got atomctl passthrough %v", got.atomctlArgs)
			}
			if got.exit != tt.wantExit {
				t.Fatalf("exit = %d, want %d", got.exit, tt.wantExit)
			}
			if got.message == "" {
				t.Errorf("short-circuit should carry a message")
			}
		})
	}
}
