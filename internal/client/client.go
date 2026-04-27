package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultOAuthEndpoint  = "https://api.customer.jp/oauth/v1"
	defaultIndigoEndpoint = "https://api.customer.jp/webarenaIndigo/v1"
)

type Client struct {
	httpClient     *http.Client
	oauthEndpoint  string
	indigoEndpoint string
	apiKey         string
	apiSecret      string
	mu             sync.Mutex
	lastRequestAt  time.Time
	minInterval    time.Duration
}

type Config struct {
	OAuthEndpoint  string
	IndigoEndpoint string
	APIKey         string
	APISecret      string
}

func New(cfg Config) *Client {
	oauthEndpoint := strings.TrimRight(cfg.OAuthEndpoint, "/")
	if oauthEndpoint == "" {
		oauthEndpoint = defaultOAuthEndpoint
	}
	indigoEndpoint := strings.TrimRight(cfg.IndigoEndpoint, "/")
	if indigoEndpoint == "" {
		indigoEndpoint = defaultIndigoEndpoint
	}
	return &Client{
		httpClient:     &http.Client{Timeout: 20 * time.Second},
		oauthEndpoint:  oauthEndpoint,
		indigoEndpoint: indigoEndpoint,
		apiKey:         cfg.APIKey,
		apiSecret:      cfg.APISecret,
		minInterval:    600 * time.Millisecond,
	}
}

type accessTokenResponse struct {
	AccessToken string `json:"accessToken"`
}

func (c *Client) token(ctx context.Context) (string, error) {
	body := map[string]string{"grantType": "client_credentials", "clientId": c.apiKey, "clientSecret": c.apiSecret, "code": ""}
	var out accessTokenResponse
	if err := c.do(ctx, http.MethodPost, c.oauthEndpoint+"/accesstokens", "", body, &out); err != nil {
		return "", fmt.Errorf("request access token: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("empty access token")
	}
	return out.AccessToken, nil
}

type APIError struct {
	StatusCode    int
	Method        string
	Endpoint      string
	Hint          string
	Message, Body string
}

func (e *APIError) Error() string {
	base := fmt.Sprintf("api error: status=%d", e.StatusCode)
	if e.Method != "" && e.Endpoint != "" {
		base = fmt.Sprintf("%s method=%s endpoint=%s", base, e.Method, e.Endpoint)
	}
	if e.Message != "" {
		if e.Hint != "" {
			return fmt.Sprintf("%s message=%s hint=%s", base, e.Message, e.Hint)
		}
		return fmt.Sprintf("%s message=%s", base, e.Message)
	}
	if s := compactBody(e.Body, 240); s != "" {
		if e.Hint != "" {
			return fmt.Sprintf("%s body=%s hint=%s", base, s, e.Hint)
		}
		return fmt.Sprintf("%s body=%s", base, s)
	}
	if e.Hint != "" {
		return fmt.Sprintf("%s hint=%s", base, e.Hint)
	}
	return base
}

func (c *Client) do(ctx context.Context, method, endpoint, token string, body any, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		payload = b
	}
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := c.waitRateLimit(ctx); err != nil {
			return err
		}
		var reader io.Reader
		if len(payload) > 0 {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if err := sleepWithContext(ctx, time.Duration(attempt+1)*200*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		apiErr := &APIError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Endpoint:   endpoint,
			Body:       string(raw),
			Message:    extractAPIErrorMessage(raw),
		}
		apiErr.Hint = errorHint(apiErr.StatusCode, apiErr.Message, apiErr.Body)

		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = apiErr
			if attempt == maxAttempts-1 {
				return apiErr
			}
			wait := retryAfter(resp.Header.Get("Retry-After"))
			if wait <= 0 {
				wait = time.Duration(attempt+1) * time.Second
			}
			if err := sleepWithContext(ctx, wait); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = apiErr
			if err := sleepWithContext(ctx, time.Duration(attempt+1)*200*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return apiErr
		}
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("decode response: %w body=%s", err, string(raw))
			}
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("request failed")
}

func (c *Client) waitRateLimit(ctx context.Context) error {
	if c.minInterval <= 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.lastRequestAt.IsZero() {
		c.lastRequestAt = now
		return nil
	}
	next := c.lastRequestAt.Add(c.minInterval)
	if now.Before(next) {
		wait := next.Sub(now)
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		now = time.Now()
	}
	c.lastRequestAt = now
	return nil
}

func extractAPIErrorMessage(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ""
	}
	values := make([]string, 0, 4)
	collectErrorMessages(decoded, &values)
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, "; ")
}

func collectErrorMessages(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		foundKnown := false
		for _, key := range []string{"message", "error", "detail", "details", "errors", "validationErrors"} {
			if val, ok := x[key]; ok {
				foundKnown = true
				collectErrorMessages(val, out)
			}
		}
		if !foundKnown {
			for _, val := range x {
				collectErrorMessages(val, out)
			}
		}
	case []any:
		for _, item := range x {
			collectErrorMessages(item, out)
		}
	case string:
		msg := strings.TrimSpace(x)
		if msg != "" {
			*out = append(*out, msg)
		}
	}
}

func compactBody(s string, limit int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

func errorHint(status int, message, body string) string {
	if status == http.StatusTooManyRequests {
		return "API rate limit exceeded. The provider retries automatically, but consider reducing parallelism (e.g. terraform apply -parallelism=1)."
	}
	if status == http.StatusBadRequest {
		msg := strings.ToUpper(message + " " + body)
		if strings.Contains(msg, "I10037") || strings.Contains(msg, "LICENSE FAILED TO UPDATE") {
			return "Indigo account/license state may be invalid. Check contract/license status on the Indigo control panel and contact support if it persists."
		}
	}
	return ""
}

func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if sec, err := strconv.Atoi(v); err == nil {
		return time.Duration(sec) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type SSHKey struct {
	ID        int
	Name      string
	PublicKey string
	Status    string
}

func (c *Client) CreateSSHKey(ctx context.Context, name, publicKey string) (*SSHKey, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	payload := map[string]string{"sshName": name, "sshKey": publicKey}
	var raw struct {
		SSHKey any `json:"sshKey"`
	}
	if err := c.do(ctx, http.MethodPost, c.indigoEndpoint+"/vm/sshkey", tok, payload, &raw); err != nil {
		return nil, err
	}
	key, err := decodeSSHKey(raw.SSHKey)
	if err != nil {
		return nil, err
	}
	if key.Name == "" {
		key.Name = name
	}
	if key.PublicKey == "" {
		key.PublicKey = publicKey
	}
	return key, nil
}

func (c *Client) GetSSHKeyByID(ctx context.Context, id int) (*SSHKey, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	var raw struct {
		SSHKey any `json:"sshKey"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/vm/sshkey/%d", c.indigoEndpoint, id), tok, nil, &raw); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	key, err := decodeSSHKey(raw.SSHKey)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func (c *Client) UpdateSSHKey(ctx context.Context, id int, name, publicKey, status string) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	payload := map[string]string{"sshName": name, "sshKey": publicKey, "sshKeyStatus": status}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("%s/vm/sshkey/%d", c.indigoEndpoint, id), tok, payload, nil)
}

func (c *Client) DeleteSSHKey(ctx context.Context, id int) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/vm/sshkey/%d", c.indigoEndpoint, id), tok, nil, nil)
}

func decodeSSHKey(v any) (*SSHKey, error) {
	if v == nil {
		return nil, fmt.Errorf("missing sshKey payload")
	}
	var key SSHKey
	if err := decodeViaMarshal(v, &key); err == nil {
		return &key, nil
	}
	var list []SSHKey
	if err := decodeViaMarshal(v, &list); err == nil && len(list) > 0 {
		return &list[0], nil
	}
	return nil, fmt.Errorf("unexpected sshKey payload format")
}

func (k *SSHKey) UnmarshalJSON(data []byte) error {
	type alias struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		PublicKey string `json:"sshkey"`
		Status    string `json:"status"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	k.ID = a.ID
	k.Name = a.Name
	k.PublicKey = a.PublicKey
	k.Status = a.Status
	return nil
}

type Instance struct {
	ID                      int
	Name, Status, RawStatus string
	RegionID, OSID, PlanID  int
	IPv4                    string
	SSHPublicKey            int
}

type CreateInstanceRequest struct {
	Name                             string
	RegionID, OSID, PlanID, SSHKeyID int
}

func (c *Client) CreateInstance(ctx context.Context, req CreateInstanceRequest) (*Instance, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"sshKeyId": req.SSHKeyID, "regionId": req.RegionID, "osId": req.OSID, "instancePlan": req.PlanID, "instanceName": req.Name}
	var raw struct {
		Success bool `json:"success"`
		VMS     any  `json:"vms"`
	}
	if err := c.do(ctx, http.MethodPost, c.indigoEndpoint+"/vm/createinstance", tok, payload, &raw); err != nil {
		return nil, err
	}
	inst, err := decodeInstance(raw.VMS)
	if err != nil {
		return nil, err
	}
	if inst.Name == "" {
		inst.Name = req.Name
	}
	return inst, nil
}

func (c *Client) GetInstanceByID(ctx context.Context, id int) (*Instance, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := c.do(ctx, http.MethodGet, c.indigoEndpoint+"/vm/getinstancelist", tok, nil, &raw); err != nil {
		return nil, err
	}
	instances, err := decodeInstanceListFromListResponse(raw)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.ID == id {
			return &inst, nil
		}
	}
	return nil, nil
}
func (c *Client) UpdateInstanceStatus(ctx context.Context, id int, status string) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, c.indigoEndpoint+"/vm/instance/statusupdate", tok, map[string]string{"instanceId": strconv.Itoa(id), "status": status}, nil)
}
func (c *Client) DeleteInstance(ctx context.Context, id int) error {
	return c.UpdateInstanceStatus(ctx, id, "destroy")
}

type InstanceType struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Client) ListInstanceTypes(ctx context.Context) ([]InstanceType, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	endpoints := []string{
		c.indigoEndpoint + "/vm/instancetypes",
		c.indigoEndpoint + "/vm/getinstancetype",
		c.indigoEndpoint + "/vm/getinstancetypelist",
		c.indigoEndpoint + "/vm/instancetype",
	}

	var lastErr error
	for _, ep := range endpoints {
		var raw struct {
			InstanceTypes any `json:"instanceTypes"`
			InstanceType  any `json:"instancetype"`
			TypeList      any `json:"typeList"`
			TypeListAlt   any `json:"instancetypelist"`
		}
		err := c.do(ctx, http.MethodGet, ep, tok, nil, &raw)
		if err != nil {
			if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusNotFound {
				lastErr = err
				continue
			}
			return nil, err
		}

		candidate := raw.InstanceTypes
		if candidate == nil {
			candidate = raw.InstanceType
		}
		if candidate == nil {
			candidate = raw.TypeList
		}
		if candidate == nil {
			candidate = raw.TypeListAlt
		}

		var types []InstanceType
		if err := decodeViaMarshal(candidate, &types); err == nil {
			return types, nil
		}
		var one InstanceType
		if err := decodeViaMarshal(candidate, &one); err == nil {
			return []InstanceType{one}, nil
		}
		lastErr = fmt.Errorf("unexpected instancetype payload format")
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("instance type endpoint not available")
}

type OS struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Client) ListOSes(ctx context.Context, instanceTypeID int) ([]OS, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(c.indigoEndpoint + "/vm/oslist")
	q := u.Query()
	q.Set("instanceTypeId", strconv.Itoa(instanceTypeID))
	u.RawQuery = q.Encode()
	var raw struct {
		OSList     any `json:"oslist"`
		OSCategory any `json:"osCategory"`
	}
	if err := c.do(ctx, http.MethodGet, u.String(), tok, nil, &raw); err != nil {
		return nil, err
	}
	candidate := raw.OSList
	if candidate == nil && raw.OSCategory != nil {
		var categories []struct {
			OSLists []OS `json:"osLists"`
		}
		if err := decodeViaMarshal(raw.OSCategory, &categories); err == nil {
			flattened := make([]OS, 0)
			for _, category := range categories {
				flattened = append(flattened, category.OSLists...)
			}
			if len(flattened) > 0 {
				return flattened, nil
			}
		}
		candidate = raw.OSCategory
	}

	var oses []OS
	if err := decodeViaMarshal(candidate, &oses); err == nil {
		return oses, nil
	}
	var one OS
	if err := decodeViaMarshal(candidate, &one); err == nil {
		return []OS{one}, nil
	}

	var categories []struct {
		OSLists []OS `json:"osLists"`
	}
	if err := decodeViaMarshal(candidate, &categories); err == nil {
		flattened := make([]OS, 0)
		for _, category := range categories {
			flattened = append(flattened, category.OSLists...)
		}
		if len(flattened) > 0 {
			return flattened, nil
		}
	}

	return nil, fmt.Errorf("unexpected os list payload format")
}

type InstanceSpec struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	CPU          int    `json:"cpu"`
	MemSize      int    `json:"memsize"`
	RootDiskSize int    `json:"rootdisksize"`
}

func (c *Client) ListInstanceSpecs(ctx context.Context, instanceTypeID, osID int) ([]InstanceSpec, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(c.indigoEndpoint + "/vm/getinstancespec")
	q := u.Query()
	q.Set("instanceTypeId", strconv.Itoa(instanceTypeID))
	q.Set("osId", strconv.Itoa(osID))
	u.RawQuery = q.Encode()
	var raw struct {
		InstanceSpec any `json:"instancespec"`
		SpecList     any `json:"specList"`
		SpecListAlt  any `json:"speclist"`
	}
	if err := c.do(ctx, http.MethodGet, u.String(), tok, nil, &raw); err != nil {
		return nil, err
	}
	candidate := raw.InstanceSpec
	if candidate == nil {
		candidate = raw.SpecList
	}
	if candidate == nil {
		candidate = raw.SpecListAlt
	}
	var specs []InstanceSpec
	if err := decodeViaMarshal(candidate, &specs); err == nil {
		return specs, nil
	}
	var one InstanceSpec
	if err := decodeViaMarshal(candidate, &one); err == nil {
		return []InstanceSpec{one}, nil
	}
	return nil, fmt.Errorf("unexpected instancespec payload format")
}

type Region struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Client) ListRegions(ctx context.Context, instanceTypeID int) ([]Region, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(c.indigoEndpoint + "/vm/getregion")
	q := u.Query()
	q.Set("instanceTypeId", strconv.Itoa(instanceTypeID))
	u.RawQuery = q.Encode()
	var raw struct {
		RegionList any `json:"regionlist"`
	}
	if err := c.do(ctx, http.MethodGet, u.String(), tok, nil, &raw); err != nil {
		return nil, err
	}
	var regions []Region
	if err := decodeViaMarshal(raw.RegionList, &regions); err != nil {
		return nil, err
	}
	return regions, nil
}

func decodeInstance(v any) (*Instance, error) {
	if v == nil {
		return nil, fmt.Errorf("missing instance payload")
	}
	var inst Instance
	if err := decodeViaMarshal(v, &inst); err == nil {
		return &inst, nil
	}
	var list []Instance
	if err := decodeViaMarshal(v, &list); err == nil && len(list) > 0 {
		return &list[0], nil
	}
	return nil, fmt.Errorf("unexpected instance payload format")
}
func decodeInstanceList(v any) ([]Instance, error) {
	if v == nil {
		return []Instance{}, nil
	}
	var list []Instance
	if err := decodeViaMarshal(v, &list); err == nil {
		return list, nil
	}
	one, err := decodeInstance(v)
	if err != nil {
		return nil, err
	}
	return []Instance{*one}, nil
}

func decodeInstanceListFromListResponse(v any) ([]Instance, error) {
	if v == nil {
		return []Instance{}, nil
	}

	var wrapped struct {
		VMS any `json:"vms"`
	}
	if err := decodeViaMarshal(v, &wrapped); err == nil && wrapped.VMS != nil {
		return decodeInstanceList(wrapped.VMS)
	}

	return decodeInstanceList(v)
}
func decodeViaMarshal(in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (i *Instance) UnmarshalJSON(data []byte) error {
	type alias struct {
		ID             int    `json:"id"`
		Name           string `json:"instance_name"`
		Status         string `json:"status"`
		InstanceStatus string `json:"instancestatus"`
		RegionID       int    `json:"region_id"`
		OSID           int    `json:"os_id"`
		PlanID         int    `json:"plan_id"`
		IPv4           string `json:"ip"`
		SSHPublicKey   int    `json:"sshkey_id"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	i.ID, i.Name, i.RegionID, i.OSID, i.PlanID, i.IPv4, i.SSHPublicKey = a.ID, a.Name, a.RegionID, a.OSID, a.PlanID, a.IPv4, a.SSHPublicKey
	i.RawStatus = a.Status
	if strings.TrimSpace(a.InstanceStatus) != "" {
		i.Status = a.InstanceStatus
	} else {
		i.Status = a.Status
	}
	return nil
}
