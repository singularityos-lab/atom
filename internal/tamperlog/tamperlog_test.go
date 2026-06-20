package tamperlog

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func newChain(t *testing.T) (*Signer, ed25519.PublicKey, []Entry) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSigner(priv)
	entries := []Entry{
		s.Append("boot ok", 1),
		s.Heartbeat(2),
		s.Append("tamper-event: /var changed", 3),
	}
	return s, pub, entries
}

func TestSignAndVerify(t *testing.T) {
	_, pub, entries := newChain(t)
	if err := NewVerifier(pub).Verify(entries); err != nil {
		t.Fatalf("a valid signed chain must verify: %v", err)
	}
}

func TestDetectTamperedPayload(t *testing.T) {
	_, pub, entries := newChain(t)
	entries[1].Payload = "forged" // change a payload after it was signed
	err := NewVerifier(pub).Verify(entries)
	if err == nil || !strings.Contains(err.Error(), "tampered") {
		t.Errorf("expected a tampered-entry error, got %v", err)
	}
}

func TestDetectSuppressionGap(t *testing.T) {
	_, pub, entries := newChain(t)
	// Drop the middle entry, as a suppressor firewalling the collector would.
	gapped := []Entry{entries[0], entries[2]}
	err := NewVerifier(pub).Verify(gapped)
	if err == nil || !strings.Contains(err.Error(), "gap") {
		t.Errorf("expected a sequence-gap error, got %v", err)
	}
}

func TestDetectWrongKey(t *testing.T) {
	_, _, entries := newChain(t)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	err := NewVerifier(otherPub).Verify(entries)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected a bad-signature error against the wrong key, got %v", err)
	}
}
