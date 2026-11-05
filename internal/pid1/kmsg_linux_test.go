package pid1

import "testing"

func TestFormatKmsg(t *testing.T) {
	got, ok := formatKmsg([]byte("6,339,5140900,-;NET: Registered protocol family 10\n SUBSYSTEM=net\n"))
	if !ok || got != "<6>[    5.140900] NET: Registered protocol family 10\n" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := formatKmsg([]byte("garbage")); ok {
		t.Fatal("garbage accepted")
	}
}
