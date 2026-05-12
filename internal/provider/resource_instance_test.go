package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// ensureStoppedForDestroyFixture は ensureStoppedForDestroy 用の httptest server を組む。
//
// power はテスト中の VM の状態 (atomic.Value で保持し、stop コマンド受信で "Stopped" に切り替える)。
// stopErrorBody が非空のときは stop コマンドに対してそのボディで 400 を返す
// (例: "already stopped" を模擬する I10017 レスポンス)。
// stopCalls は statusupdate へのリクエスト数 (テストで「呼ばれていないこと」を検証するため)。
type ensureStoppedForDestroyFixture struct {
	server        *httptest.Server
	client        *client.Client
	stopCalls     *int32
	power         *atomic.Value // string
	stopErrorBody string
}

func newEnsureStoppedForDestroyFixture(t *testing.T, initialPower string, instanceMissing bool, stopErrorBody string) *ensureStoppedForDestroyFixture {
	t.Helper()

	power := &atomic.Value{}
	power.Store(initialPower)
	var stopCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/getinstancelist", func(w http.ResponseWriter, r *http.Request) {
		if instanceMissing {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":             99,
			"instance_name":  "fixture",
			"status":         "OPEN",
			"instancestatus": power.Load().(string),
			"os_id":          22,
			"plan_id":        13,
			"ipaddress":      "198.51.100.10",
			"sshkey_id":      42,
		}})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/instance/statusupdate", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stopCalls, 1)
		if stopErrorBody != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(stopErrorBody))
			return
		}
		// stop コマンド受信 → power を Stopped に遷移させる
		power.Store("Stopped")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	})

	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)

	c := client.New(client.Config{
		APIKey: "k", APISecret: "s",
		OAuthEndpoint:  s.URL + "/oauth/v1",
		IndigoEndpoint: s.URL + "/webarenaIndigo/v1",
	})
	return &ensureStoppedForDestroyFixture{
		server:    s,
		client:    c,
		stopCalls: &stopCalls,
		power:     power,
	}
}

func TestEnsureStoppedForDestroy_Running(t *testing.T) {
	f := newEnsureStoppedForDestroyFixture(t, "Running", false, "")
	if err := ensureStoppedForDestroy(context.Background(), f.client, 99); err != nil {
		t.Fatalf("ensureStoppedForDestroy: %v", err)
	}
	if got := atomic.LoadInt32(f.stopCalls); got != 1 {
		t.Fatalf("stop call count = %d, want 1", got)
	}
	if got := f.power.Load().(string); got != "Stopped" {
		t.Fatalf("power = %q, want Stopped", got)
	}
}

func TestEnsureStoppedForDestroy_AlreadyStopped(t *testing.T) {
	f := newEnsureStoppedForDestroyFixture(t, "Stopped", false, "")
	if err := ensureStoppedForDestroy(context.Background(), f.client, 99); err != nil {
		t.Fatalf("ensureStoppedForDestroy: %v", err)
	}
	if got := atomic.LoadInt32(f.stopCalls); got != 0 {
		t.Fatalf("stop call count = %d, want 0 (must not call statusupdate)", got)
	}
}

func TestEnsureStoppedForDestroy_InstanceGone(t *testing.T) {
	f := newEnsureStoppedForDestroyFixture(t, "Running", true, "")
	if err := ensureStoppedForDestroy(context.Background(), f.client, 99); err != nil {
		t.Fatalf("ensureStoppedForDestroy: %v", err)
	}
	if got := atomic.LoadInt32(f.stopCalls); got != 0 {
		t.Fatalf("stop call count = %d, want 0 (instance gone)", got)
	}
}

// TestEnsureStoppedForDestroy_AlreadyStoppedRace は Read で RUNNING を観測した直後に
// 外部から stop されたケースを模擬する。stop コマンドが I10017 で 400 を返してきても、
// その後 getinstancelist で STOPPED を確認できれば成功扱いになることを検証する。
//
// 既存 TestIsIdempotentStatusUpdateError は関数単体テストなので、ensureStoppedForDestroy
// 経由でこのレース許容が壊れる regression は別に検出する必要がある。
func TestEnsureStoppedForDestroy_AlreadyStoppedRace(t *testing.T) {
	// stop コマンドは I10017 を返すが、stop ハンドラ呼び出し時に power を Stopped に変えて
	// 後続の getinstancelist が STOPPED を返すようにする
	power := &atomic.Value{}
	power.Store("Running")
	var stopCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/getinstancelist", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":             99,
			"instance_name":  "fixture",
			"status":         "OPEN",
			"instancestatus": power.Load().(string),
		}})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/instance/statusupdate", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stopCalls, 1)
		power.Store("Stopped") // out-of-band stop が同時に走った想定
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "This instance is already stopped.; I10017"})
	})
	s := httptest.NewServer(mux)
	defer s.Close()

	c := client.New(client.Config{
		APIKey: "k", APISecret: "s",
		OAuthEndpoint:  s.URL + "/oauth/v1",
		IndigoEndpoint: s.URL + "/webarenaIndigo/v1",
	})
	if err := ensureStoppedForDestroy(context.Background(), c, 99); err != nil {
		t.Fatalf("ensureStoppedForDestroy: %v", err)
	}
	if got := atomic.LoadInt32(&stopCalls); got != 1 {
		t.Fatalf("stop call count = %d, want 1", got)
	}
}

func TestEnsureStoppedForDestroy_TransientState(t *testing.T) {
	f := newEnsureStoppedForDestroyFixture(t, "OS installation In Progress", false, "")
	err := ensureStoppedForDestroy(context.Background(), f.client, 99)
	if err == nil {
		t.Fatal("expected error for transient state")
	}
	if !strings.Contains(err.Error(), "transient state") {
		t.Fatalf("error must mention transient state, got: %v", err)
	}
	if got := atomic.LoadInt32(f.stopCalls); got != 0 {
		t.Fatalf("stop call count = %d, want 0 (must not call statusupdate in transient state)", got)
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
