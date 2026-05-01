package provider

import (
	"fmt"
	"testing"

	"github.com/ginokent/terraform-provider-indigo/internal/client"
)

func TestNormalizePowerStatus(t *testing.T) {
	cases := map[string]string{
		// 実 API で観測される power 値 (case-insensitive で受けて UPPER_CASE で返す)
		"Running": "RUNNING",
		"running": "RUNNING",
		"RUNNING": "RUNNING",
		"Stopped": "STOPPED",
		"stopped": "STOPPED",
		"STOPPED": "STOPPED",
		// 遷移中文字列はそのまま (uppercased+trimmed)
		"OS installation In Progress": "OS INSTALLATION IN PROGRESS",
		"  Running  ":                 "RUNNING",
		"":                            "",
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

	statusSchema, ok := r.Schema["status"]
	if !ok {
		t.Fatal("status schema must exist")
	}
	if !statusSchema.Computed || statusSchema.Optional {
		t.Fatal("status must be computed-only API status")
	}

	instanceStatusSchema, ok := r.Schema["instance_status"]
	if !ok {
		t.Fatal("instance_status schema must exist")
	}
	if !instanceStatusSchema.Optional || !instanceStatusSchema.Computed {
		t.Fatal("instance_status must be optional+computed desired/observed state")
	}
}
