package provider

import "testing"

func TestNormalizePowerStatus(t *testing.T) {
	cases := map[string]string{
		"running":   "running",
		"RUNNING":   "running",
		"start":     "running",
		"open":      "stopped",
		"active":    "running",
		"ready":     "running",
		"stopped":   "stopped",
		"STOP":      "stopped",
		"forcestop": "stopped",
		"closed":    "stopped",
	}
	for in, want := range cases {
		if got := normalizePowerStatus(in); got != want {
			t.Fatalf("normalizePowerStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResourceInstanceSupportsUpdate(t *testing.T) {
	r := resourceInstance()
	if r.UpdateContext == nil {
		t.Fatal("resourceInstance must support update for power state transitions")
	}
	if r.Update != nil {
		t.Fatal("resourceInstance must not set both Update and UpdateContext")
	}
}
