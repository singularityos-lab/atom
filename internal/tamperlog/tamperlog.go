package tamperlog

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// HeartbeatPayload is the conventional payload of a liveness entry.
const HeartbeatPayload = "\x00heartbeat"

// Entry is one signed, chained log record.
type Entry struct {
	Seq          uint64 `json:"seq"`
	PrevHash     []byte `json:"prev"`
	TimeUnixNano int64  `json:"t"`
	Payload      string `json:"payload"`
	Hash         []byte `json:"hash"`
	Sig          []byte `json:"sig"`
}

// computeHash binds the sequence, the previous hash, the timestamp, and the
// payload into the entry hash. Domain-separated by field with fixed-width
// integers so distinct inputs cannot collide via concatenation ambiguity.
func computeHash(seq uint64, prev []byte, t int64, payload string) []byte {
	h := sha256.New()
	_ = binary.Write(h, binary.BigEndian, seq)
	_ = binary.Write(h, binary.BigEndian, uint32(len(prev)))
	h.Write(prev)
	_ = binary.Write(h, binary.BigEndian, t)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

// Signer produces a chain of signed entries with the device key.
type Signer struct {
	priv ed25519.PrivateKey
	seq  uint64
	prev []byte
}

// NewSigner returns a signer starting an empty chain.
func NewSigner(priv ed25519.PrivateKey) *Signer {
	return &Signer{priv: priv}
}

// Append chains and signs a new entry for the payload at the given timestamp.
func (s *Signer) Append(payload string, nowUnixNano int64) Entry {
	s.seq++
	h := computeHash(s.seq, s.prev, nowUnixNano, payload)
	e := Entry{
		Seq:          s.seq,
		PrevHash:     s.prev,
		TimeUnixNano: nowUnixNano,
		Payload:      payload,
		Hash:         h,
		Sig:          ed25519.Sign(s.priv, h),
	}
	s.prev = h
	return e
}

// Heartbeat appends a liveness entry.
func (s *Signer) Heartbeat(nowUnixNano int64) Entry {
	return s.Append(HeartbeatPayload, nowUnixNano)
}

// Verifier checks a chain against the device public key.
type Verifier struct {
	pub ed25519.PublicKey
}

// NewVerifier returns a verifier for the given public key.
func NewVerifier(pub ed25519.PublicKey) *Verifier { return &Verifier{pub: pub} }

// Verify checks a contiguous slice of entries: sequence continuity (no gaps),
// chain linkage (each PrevHash matches the prior Hash), payload integrity (the
// recomputed hash matches), and the Ed25519 signature. It returns a descriptive
// error naming the first problem, so a suppression gap or a tampered entry is
// pinpointed.
func (v *Verifier) Verify(entries []Entry) error {
	var prev []byte
	var lastSeq uint64
	for i, e := range entries {
		if i == 0 {
			lastSeq = e.Seq - 1
		}
		if e.Seq != lastSeq+1 {
			return fmt.Errorf("sequence gap: expected seq %d, got %d (entries dropped/suppressed)", lastSeq+1, e.Seq)
		}
		if !bytes.Equal(e.PrevHash, prev) {
			return fmt.Errorf("broken chain at seq %d: prev hash mismatch", e.Seq)
		}
		want := computeHash(e.Seq, e.PrevHash, e.TimeUnixNano, e.Payload)
		if !bytes.Equal(want, e.Hash) {
			return fmt.Errorf("tampered entry at seq %d: hash mismatch", e.Seq)
		}
		if !ed25519.Verify(v.pub, e.Hash, e.Sig) {
			return fmt.Errorf("bad signature at seq %d", e.Seq)
		}
		prev = e.Hash
		lastSeq = e.Seq
	}
	return nil
}
