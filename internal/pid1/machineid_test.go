package pid1

import "testing"

func TestRandomMachineID(t *testing.T) {
	id, err := randomMachineID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 {
		t.Errorf("machine-id length = %d, want 32 hex chars", len(id))
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("machine-id contains non-lowercase-hex %q", c)
		}
	}
	if id2, _ := randomMachineID(); id == id2 {
		t.Error("machine-id is not random (two generations matched)")
	}
}
