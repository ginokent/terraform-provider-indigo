package provider

import (
	"fmt"
	"testing"

	"github.com/example/terraform-provider-webarena-indigo/internal/client"
)

func TestNormalizePowerStatus(t *testing.T) {
	cases := map[string]string{
		"running":   "running",
		"RUNNING":   "running",
		"Running":   "running",
		"start":     "running",
		"open":      "stopped",
		"active":    "running",
		"ready":     "running",
		"stopped":   "stopped",
		"Stopped":   "stopped",
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

func TestIsIdempotentStatusUpdateError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		err     error
		command string
		want    bool
	}{
		{
			name:    "start_already_running",
			err:     &client.APIError{StatusCode: 400, Message: "This instance is already running.; I10016"},
			command: "start",
			want:    true,
		},
		{
			name:    "stop_already_stopped",
			err:     &client.APIError{StatusCode: 400, Message: "This instance is already stopped.; I10017"},
			command: "stop",
			want:    true,
		},
		{
			name:    "wrong_command",
			err:     &client.APIError{StatusCode: 400, Message: "This instance is already running.; I10016"},
			command: "stop",
			want:    false,
		},
		{
			name:    "non_bad_request",
			err:     &client.APIError{StatusCode: 500, Message: "This instance is already running.; I10016"},
			command: "start",
			want:    false,
		},
		{
			name:    "non_api_error",
			err:     fmt.Errorf("plain error"),
			command: "start",
			want:    false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isIdempotentStatusUpdateError(tc.err, tc.command)
			if got != tc.want {
				t.Fatalf("isIdempotentStatusUpdateError() = %v, want %v", got, tc.want)
			}
		})
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
