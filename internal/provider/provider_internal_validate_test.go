package provider

import "testing"

func TestProviderInternalValidate(t *testing.T) {
	p := New()
	if err := p.InternalValidate(); err != nil {
		t.Fatalf("provider internal validation failed: %v", err)
	}
}
