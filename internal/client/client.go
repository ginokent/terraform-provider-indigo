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
	Message, Body string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("api error: status=%d message=%s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("api error: status=%d", e.StatusCode)
}

func (c *Client) do(ctx context.Context, method, endpoint, token string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	var raw []byte
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
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
			time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
			continue
		}
		raw, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server error %d", resp.StatusCode)
			time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
			continue
		}
		if resp.StatusCode >= 400 {
			apiErr := &APIError{StatusCode: resp.StatusCode, Body: string(raw)}
			var msg struct{ Message, Error string }
			if json.Unmarshal(raw, &msg) == nil {
				if msg.Message != "" {
					apiErr.Message = msg.Message
				} else {
					apiErr.Message = msg.Error
				}
			}
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
	ID                     int
	Name, Status           string
	RegionID, OSID, PlanID int
	IPv4                   string
	SSHPublicKey           int
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
	var raw struct {
		VMS any `json:"vms"`
	}
	if err := c.do(ctx, http.MethodGet, c.indigoEndpoint+"/vm/getinstancelist", tok, nil, &raw); err != nil {
		return nil, err
	}
	instances, err := decodeInstanceList(raw.VMS)
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
		c.indigoEndpoint + "/vm/getinstancetype",
		c.indigoEndpoint + "/vm/getinstancetypelist",
		c.indigoEndpoint + "/vm/instancetype",
	}

	var lastErr error
	for _, ep := range endpoints {
		var raw struct {
			InstanceType any `json:"instancetype"`
			TypeList     any `json:"typeList"`
			TypeListAlt  any `json:"instancetypelist"`
		}
		err := c.do(ctx, http.MethodGet, ep, tok, nil, &raw)
		if err != nil {
			if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusNotFound {
				lastErr = err
				continue
			}
			return nil, err
		}

		candidate := raw.InstanceType
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
		OSList any `json:"oslist"`
	}
	if err := c.do(ctx, http.MethodGet, u.String(), tok, nil, &raw); err != nil {
		return nil, err
	}
	var oses []OS
	if err := decodeViaMarshal(raw.OSList, &oses); err == nil {
		return oses, nil
	}
	var one OS
	if err := decodeViaMarshal(raw.OSList, &one); err == nil {
		return []OS{one}, nil
	}
	return nil, fmt.Errorf("unexpected oslist payload format")
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
	}
	if err := c.do(ctx, http.MethodGet, u.String(), tok, nil, &raw); err != nil {
		return nil, err
	}
	candidate := raw.InstanceSpec
	if candidate == nil {
		candidate = raw.SpecList
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
func decodeViaMarshal(in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (i *Instance) UnmarshalJSON(data []byte) error {
	type alias struct {
		ID           int    `json:"id"`
		Name         string `json:"instance_name"`
		Status       string `json:"status"`
		RegionID     int    `json:"region_id"`
		OSID         int    `json:"os_id"`
		PlanID       int    `json:"plan_id"`
		IPv4         string `json:"ip"`
		SSHPublicKey int    `json:"sshkey_id"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	i.ID, i.Name, i.Status, i.RegionID, i.OSID, i.PlanID, i.IPv4, i.SSHPublicKey = a.ID, a.Name, a.Status, a.RegionID, a.OSID, a.PlanID, a.IPv4, a.SSHPublicKey
	return nil
}
