package pons

import (
	"errors"
	"testing"
)

func TestIsAlreadyKnown(t *testing.T) {
	if IsAlreadyKnown(nil) {
		t.Fatal("nil is not already-known")
	}
	for _, msg := range []string{
		"already known",
		"ALREADY KNOWN",
		"nonce too low",
		"known transaction: 0xabc",
	} {
		if !IsAlreadyKnown(errors.New(msg)) {
			t.Fatalf("%q should be ignorable", msg)
		}
	}
	if IsAlreadyKnown(errors.New("insufficient funds")) {
		t.Fatal("insufficient funds is not already-known")
	}
}
