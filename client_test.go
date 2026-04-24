package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateAndGetInstance_WithInconsistentPayloadShapes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/createinstance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"vms": map[string]any{
				"id":            99,
				"instance_name": "my-vm",
				"status":        "READY",
				"region_id":     1,
				"os_id":         22,
				"plan_id":       13,
				"ip":            "198.51.100.10",
				"sshkey_id":     42,
			},
		})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/getinstancelist", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"vms": []map[string]any{{
				"id":            99,
				"instance_name": "my-vm",
				"status":        "running",
				"region_id":     1,
				"os_id":         22,
				"plan_id":       13,
				"ip":            "198.51.100.10",
				"sshkey_id":     42,
			}},
		})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/instance/statusupdate", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "sucessCode": "I20009"})
	})

	s := httptest.NewServer(mux)
	defer s.Close()

	c := New(Config{APIKey: "k", APISecret: "s", OAuthEndpoint: s.URL + "/oauth/v1", IndigoEndpoint: s.URL + "/webarenaIndigo/v1"})
	inst, err := c.CreateInstance(context.Background(), CreateInstanceRequest{Name: "my-vm", RegionID: 1, OSID: 22, PlanID: 13, SSHKeyID: 42})
	if err != nil {
		t.Fatalf("CreateInstance failed: %v", err)
	}
	if inst.ID != 99 {
		t.Fatalf("expected id 99, got %d", inst.ID)
	}

	got, err := c.GetInstanceByID(context.Background(), 99)
	if err != nil {
		t.Fatalf("GetInstanceByID failed: %v", err)
	}
	if got == nil || got.Status != "running" {
		t.Fatalf("unexpected instance payload: %#v", got)
	}

	if err := c.UpdateInstanceStatus(context.Background(), 99, "stop"); err != nil {
		t.Fatalf("UpdateInstanceStatus failed: %v", err)
	}
}

func TestAPIError_WithMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Invalid client credentials"}`))
	})
	s := httptest.NewServer(mux)
	defer s.Close()

	c := New(Config{APIKey: "bad", APISecret: "bad", OAuthEndpoint: s.URL + "/oauth/v1", IndigoEndpoint: s.URL + "/webarenaIndigo/v1"})
	_, err := c.token(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() == "" || !strings.Contains(err.Error(), "Invalid client credentials") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetInstanceByID_BadJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/getinstancelist", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"vms":[{"id":`))
	})
	s := httptest.NewServer(mux)
	defer s.Close()

	c := New(Config{APIKey: "k", APISecret: "s", OAuthEndpoint: s.URL + "/oauth/v1", IndigoEndpoint: s.URL + "/webarenaIndigo/v1"})
	_, err := c.GetInstanceByID(context.Background(), 1)
	if err == nil {
		t.Fatal("expected json error")
	}
}
