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
