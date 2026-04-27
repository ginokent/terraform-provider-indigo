package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
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
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":            99,
			"instance_name": "my-vm",
			"status":        "running",
			"region_id":     1,
			"os_id":         22,
			"plan_id":       13,
			"ip":            "198.51.100.10",
			"sshkey_id":     42,
		}})
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

func TestAPIError_WithValidationErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/sshkey", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "Validation failed",
			"errors": map[string]any{
				"sshKey": []string{"sshKey format is invalid"},
			},
		})
	})

	s := httptest.NewServer(mux)
	defer s.Close()

	c := New(Config{APIKey: "k", APISecret: "s", OAuthEndpoint: s.URL + "/oauth/v1", IndigoEndpoint: s.URL + "/webarenaIndigo/v1"})
	_, err := c.CreateSSHKey(context.Background(), "Example", "not-a-public-key")
	if err == nil {
		t.Fatal("expected validation error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Validation failed") || !strings.Contains(msg, "sshKey format is invalid") {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if !strings.Contains(msg, "method=POST") || !strings.Contains(msg, "/vm/sshkey") {
		t.Fatalf("missing request context in error: %v", err)
	}
}

func TestAPIError_WithKnownLicenseFailureHint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/sshkey", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "License Failed To Update.",
			"error":   "I10037",
		})
	})

	s := httptest.NewServer(mux)
	defer s.Close()

	c := New(Config{APIKey: "k", APISecret: "s", OAuthEndpoint: s.URL + "/oauth/v1", IndigoEndpoint: s.URL + "/webarenaIndigo/v1"})
	_, err := c.CreateSSHKey(context.Background(), "Example", "ssh-rsa AAA")
	if err == nil {
		t.Fatal("expected license error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "I10037") || !strings.Contains(msg, "hint=") {
		t.Fatalf("expected code and hint in error: %v", err)
	}
}

func TestListInstanceTypes_RetryOn429(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	attempt := 0
	mux.HandleFunc("/webarenaIndigo/v1/vm/instancetypes", func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Too Many Request"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"instanceTypes": []map[string]any{{"id": 1, "name": "KVM"}}})
	})

	s := httptest.NewServer(mux)
	defer s.Close()

	c := New(Config{APIKey: "k", APISecret: "s", OAuthEndpoint: s.URL + "/oauth/v1", IndigoEndpoint: s.URL + "/webarenaIndigo/v1"})
	c.minInterval = 0

	start := time.Now()
	types, err := c.ListInstanceTypes(context.Background())
	if err != nil {
		t.Fatalf("ListInstanceTypes failed after retry: %v", err)
	}
	if len(types) != 1 || types[0].ID != 1 {
		t.Fatalf("unexpected types: %#v", types)
	}
	if attempt != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempt)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("expected retry-after wait, elapsed=%s", elapsed)
	}
}

func TestRetryAfter(t *testing.T) {
	if got := retryAfter("2"); got != 2*time.Second {
		t.Fatalf("expected 2s, got %s", got)
	}
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	got := retryAfter(future)
	if got <= 0 {
		t.Fatalf("expected positive duration for http-date, got %s", got)
	}
	if got := retryAfter(strconv.Itoa(0)); got != 0 {
		t.Fatalf("expected 0 for Retry-After: 0, got %s", got)
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

func TestInstanceUnmarshal_UsesInstanceStatusWhenPresent(t *testing.T) {
	var inst Instance
	payload := []byte(`{
		"id": 779400,
		"instance_name": "tf-indigo-vm",
		"status": "OPEN",
		"instancestatus": "Running",
		"region_id": 1,
		"os_id": 25,
		"plan_id": 3,
		"ipaddress": "116.80.48.236",
		"ip": "198.51.100.10",
		"sshkey_id": 45985
	}`)

	if err := json.Unmarshal(payload, &inst); err != nil {
		t.Fatalf("unmarshal instance failed: %v", err)
	}
	if inst.APIStatus != "OPEN" {
		t.Fatalf("APIStatus = %q, want OPEN", inst.APIStatus)
	}
	if inst.Status != "Running" {
		t.Fatalf("Status = %q, want Running", inst.Status)
	}
	if inst.IPv4 != "116.80.48.236" {
		t.Fatalf("IPv4 = %q, want 116.80.48.236", inst.IPv4)
	}
}

func TestSSHKeyCRUD_WithArrayAndObjectShapes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/sshkey", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "sshKey": map[string]any{"id": 892, "name": "Example", "sshkey": "ssh-rsa AAA", "status": "ACTIVE"}})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/sshkey/892", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "sshKey": []map[string]any{{"id": 892, "name": "Example", "sshkey": "ssh-rsa AAA", "status": "ACTIVE"}}})
		case http.MethodPut:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		case http.MethodDelete:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	s := httptest.NewServer(mux)
	defer s.Close()

	c := New(Config{APIKey: "k", APISecret: "s", OAuthEndpoint: s.URL + "/oauth/v1", IndigoEndpoint: s.URL + "/webarenaIndigo/v1"})
	key, err := c.CreateSSHKey(context.Background(), "Example", "ssh-rsa AAA")
	if err != nil {
		t.Fatalf("CreateSSHKey failed: %v", err)
	}
	if key.ID != 892 {
		t.Fatalf("expected ssh key id 892, got %d", key.ID)
	}

	got, err := c.GetSSHKeyByID(context.Background(), 892)
	if err != nil {
		t.Fatalf("GetSSHKeyByID failed: %v", err)
	}
	if got == nil || got.Name != "Example" {
		t.Fatalf("unexpected ssh key payload: %#v", got)
	}

	if err := c.UpdateSSHKey(context.Background(), 892, "Example", "ssh-rsa AAA", "ACTIVE"); err != nil {
		t.Fatalf("UpdateSSHKey failed: %v", err)
	}
	if err := c.DeleteSSHKey(context.Background(), 892); err != nil {
		t.Fatalf("DeleteSSHKey failed: %v", err)
	}
}

func TestListOSesAndInstanceSpecs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/oslist", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"osCategory": []map[string]any{{
				"id":   1,
				"name": "Ubuntu",
				"osLists": []map[string]any{
					{"id": 22, "name": "ubuntu-22.04"},
				},
			}},
		})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/getinstancespec", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"speclist": []map[string]any{{"id": 13, "name": "small", "cpu": 2, "memsize": 4096, "rootdisksize": 40}}})
	})

	s := httptest.NewServer(mux)
	defer s.Close()
	c := New(Config{APIKey: "k", APISecret: "s", OAuthEndpoint: s.URL + "/oauth/v1", IndigoEndpoint: s.URL + "/webarenaIndigo/v1"})

	oses, err := c.ListOSes(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListOSes failed: %v", err)
	}
	if len(oses) != 1 || oses[0].ID != 22 {
		t.Fatalf("unexpected os list: %#v", oses)
	}

	specs, err := c.ListInstanceSpecs(context.Background(), 1, 22)
	if err != nil {
		t.Fatalf("ListInstanceSpecs failed: %v", err)
	}
	if len(specs) != 1 || specs[0].ID != 13 {
		t.Fatalf("unexpected specs list: %#v", specs)
	}
}

func TestListInstanceTypes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/accesstokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
	})
	mux.HandleFunc("/webarenaIndigo/v1/vm/instancetypes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"instanceTypes": []map[string]any{{"id": 1, "name": "KVM"}}})
	})

	s := httptest.NewServer(mux)
	defer s.Close()
	c := New(Config{APIKey: "k", APISecret: "s", OAuthEndpoint: s.URL + "/oauth/v1", IndigoEndpoint: s.URL + "/webarenaIndigo/v1"})

	types, err := c.ListInstanceTypes(context.Background())
	if err != nil {
		t.Fatalf("ListInstanceTypes failed: %v", err)
	}
	if len(types) != 1 || types[0].ID != 1 {
		t.Fatalf("unexpected instance types: %#v", types)
	}
}
