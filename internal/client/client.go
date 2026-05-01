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

// Client は Indigo API への HTTP クライアント。
//
// レート制御は Indigo 独自のレスポンスヘッダ駆動:
//   - x-quota-allowed   : ウィンドウ内の最大リクエスト数
//   - x-quota-available : ウィンドウ内の残量
//   - x-quota-reset     : 次にクォータが復活する時刻 (Unix epoch ms)
//
// 静的な minInterval を持たないのは、観測される quota 上限 (例: 6 req/min) が
// アカウントや時間帯で変動しうるため。実測値で動的にスロットルする方が頑健。
type Client struct {
	httpClient     *http.Client
	oauthEndpoint  string
	indigoEndpoint string
	apiKey         string
	apiSecret      string

	// quotaMu は quotaResetAt / quotaAvailable と、それらに従う待機の排他に使う。
	// ヘッダ観測 / 待機 / 待機後の更新が同一インスタンスの全リクエストで直列化される必要があるため、
	// httpClient の並列性とは別の mutex として持つ。
	quotaMu        sync.Mutex
	quotaResetAt   time.Time // x-quota-reset から観測した次のクォータ復活時刻 (zero = 未観測)
	quotaAvailable int       // 直近観測した x-quota-available (-1 = 未観測)
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
		quotaAvailable: -1,
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

// APIError は Indigo API からの非 2xx レスポンスを表す。
//
// Hint はエラーの一次情報ではなく、Indigo 固有の状況 (rate limit / license 失効など) を
// 検出したとき、上位レイヤがユーザに表示するための追加ガイダンス。errorHint で生成する。
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
		// レスポンスヘッダから quota 状態を観測。429 / 成功 / 他エラーすべてで更新する
		// (Indigo は 2xx レスポンスにも x-quota-* を載せてくるため、予防的スロットルに使える)。
		c.recordQuota(resp.Header)
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
			// 待機優先順位: Retry-After (RFC 標準) → x-quota-reset (Indigo 独自) → 線形 fallback。
			// Indigo は通常 Retry-After を返さず x-quota-reset のみ返すが、将来仕様変更で
			// Retry-After が来た場合はそちらを尊重する。
			wait := retryAfter(resp.Header.Get("Retry-After"))
			if wait <= 0 {
				if reset := parseQuotaReset(resp.Header.Get("x-quota-reset")); !reset.IsZero() {
					wait = time.Until(reset) + 50*time.Millisecond
				}
			}
			if wait <= 0 {
				wait = time.Duration(attempt+1) * time.Second
			}
			if err := sleepWithContext(ctx, wait); err != nil {
				return err
			}
			// 待機で quota window の復活を期待しているので、recordQuota が記録した
			// avail=0/reset=過去 の値はもう古い。クリアしないと次の waitRateLimit が
			// 同じ reset まで再度待機する二重待機を引き起こす。
			c.clearQuota()
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

// waitRateLimit は直近観測した quota 状態に基づいて、必要なら次のクォータ復活時刻まで待機する。
//
// 待機条件: x-quota-available が 1 以下 ("0" のとき送れば確実に 429、"1" のときも複数並列で
// 送れば 429 になる) かつ x-quota-reset が未来。条件を満たせば reset+50ms (jitter) まで sleep する。
//
// quotaMu を握ったまま sleep する: そうしないと並列リクエストが quotaAvailable=0 を見て
// 全部同時に sleep に入り、reset 直後に殺到して再度 429 を踏む thundering-herd になる。
// quotaMu はクライアント全体のシリアライズではなく、quota 復活待ちのコーディネーションのためのもの。
func (c *Client) waitRateLimit(ctx context.Context) error {
	c.quotaMu.Lock()
	defer c.quotaMu.Unlock()

	if c.quotaAvailable < 0 || c.quotaAvailable > 1 || c.quotaResetAt.IsZero() {
		return nil
	}
	wait := time.Until(c.quotaResetAt) + 50*time.Millisecond
	if wait > 0 {
		if err := sleepWithContext(ctx, wait); err != nil {
			return err
		}
	}
	// reset が既に過去 / 待機完了のいずれの場合も、観測値はもう古い。
	// 次のリクエストの応答で recordQuota が再度埋めるまでクリア。
	c.clearQuotaLocked()
	return nil
}

// clearQuota は観測済みの quota 状態を破棄する。
// 429 直後の待機で window 復活が期待される場合、過去の avail=0/reset=now+x の値で
// waitRateLimit が再度同じ wait を引き起こすのを防ぐために呼ぶ。
func (c *Client) clearQuota() {
	c.quotaMu.Lock()
	defer c.quotaMu.Unlock()
	c.clearQuotaLocked()
}

// clearQuotaLocked は呼び出し側が quotaMu を保持している前提で観測値を破棄する。
func (c *Client) clearQuotaLocked() {
	c.quotaAvailable = -1
	c.quotaResetAt = time.Time{}
}

// recordQuota はレスポンスヘッダから観測した quota 状態を保存する。
// x-quota-available と x-quota-reset の両方が来たときだけ更新する (片方欠ければ無視)。
func (c *Client) recordQuota(h http.Header) {
	avail := parseQuotaAvailable(h.Get("x-quota-available"))
	reset := parseQuotaReset(h.Get("x-quota-reset"))
	if avail < 0 || reset.IsZero() {
		return
	}
	c.quotaMu.Lock()
	c.quotaAvailable = avail
	c.quotaResetAt = reset
	c.quotaMu.Unlock()
}

// parseQuotaReset は x-quota-reset (Unix epoch ms 文字列) を time.Time に変換する。
// 不正値は zero time を返し、呼び出し側で「未観測」として扱える。
func parseQuotaReset(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// parseQuotaAvailable は x-quota-available を int に変換する。
// 不正値は -1 を返し、呼び出し側で「未観測」として扱える。
func parseQuotaAvailable(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// extractAPIErrorMessage は Indigo のエラーレスポンスから人間可読なメッセージを抽出する。
//
// Indigo はエンドポイントによって {"message":"..."} / {"error":"..."} /
// {"errors":[{"detail":"..."}]} / {"validationErrors":{...}} 等まちまちの形でエラーを返すため、
// 既知のキー名を順に探索し、それでも見つからなければマップ全体を再帰的に走査する。
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

// errorHint は Indigo 特有の状況に対するユーザ向けガイダンスを生成する。
// 該当しない場合は空文字を返し、上位は通常のエラーメッセージのみを表示する。
//
// 扱う case:
//   - 429: クォータウィンドウ内のリクエスト枠を使い切った状況。provider は x-quota-reset まで待ってリトライするが、
//     リトライ予算 (maxAttempts) を超えても解消しない場合は parallelism を下げるか枠の引き上げが必要
//   - 400 + I10037 / "license failed to update": Indigo 側の契約/ライセンス状態異常 (運用支援が必要)
func errorHint(status int, message, body string) string {
	if status == http.StatusTooManyRequests {
		return "API quota exhausted. The provider waits until the quota window resets (x-quota-reset) before retrying. If this persists, reduce concurrency (e.g. terraform apply -parallelism=1) or contact Indigo support to raise the quota."
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

// decodeSSHKey は Indigo の sshKey フィールドを単体でも配列でも受け付ける。
// CreateSSHKey は配列、GetSSHKeyByID は単体オブジェクトを返してくる等
// 同名キーで shape が変わるため、両方試して通った方を採用する。
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

// Instance は Indigo の VM 情報を表す。
//
// LifecycleStatus と PowerStatus は別概念であり混同してはいけない:
//   - LifecycleStatus (API: "status")  : リソース管理面の状態。観測値は "READY" (provisioning 中) / "OPEN" (provisioning 完了)
//   - PowerStatus     (API: "instancestatus"): VM の電源/動作状態。観測値は "Running" / "Stopped" / "OS installation In Progress" 等
//
// API は Region ID を返さない (regionname だけ返してくる) ため、構造体にも持たない。
// Region ID はユーザが create 時に与える immutable な入力で、Terraform state 側で保持する。
type Instance struct {
	ID              int
	Name            string
	LifecycleStatus string
	PowerStatus     string
	OSID, PlanID    int
	IPv4            string
	SSHKeyID        int
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
	var instances []Instance
	if err := c.do(ctx, http.MethodGet, c.indigoEndpoint+"/vm/getinstancelist", tok, nil, &instances); err != nil {
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
// DeleteInstance は status コマンド destroy で削除を要求する。
// Indigo には DELETE 専用エンドポイントが存在せず、statusupdate 経由で destroy を投げるのが正攻法。
func (c *Client) DeleteInstance(ctx context.Context, id int) error {
	return c.UpdateInstanceStatus(ctx, id, "destroy")
}

type InstanceType struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ListInstanceTypes は instance type 一覧を返す。
//
// Indigo の API ドキュメントとデプロイ環境で path / レスポンスキーが揺れているため、
// endpoint 候補を順に試行し 404 はスキップ、最初に成功したレスポンスを採用する。
// レスポンスキーも instanceTypes / instancetype / typeList / instancetypelist のいずれかが返るため
// raw 構造体で全候補を受け取り、非 nil の最初のものを使う。
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

// decodeInstance は Indigo の vms/instance フィールドを単体でも配列でも受け付ける。
// CreateInstance のレスポンスは環境によって object / array が揺れるため両方試す。
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

// decodeViaMarshal は any (json.Unmarshal で得た map/slice) を out の型に再アサインするためのユーティリティ。
// Indigo はレスポンス shape が一定でない (key 名/object-array) ため、いったん any で受けてから
// 候補の Go 型に対し Marshal→Unmarshal を試行する方針を採っている。
func decodeViaMarshal(in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// UnmarshalJSON は Indigo の Instance レスポンスを正規化する。
//
// 手書きしている理由:
//   - lifecycle ("status") と power ("instancestatus") は **別概念であり、絶対に混同しない**。
//     どちらか一方が空でももう一方の値を流用してはならない。
//   - IP は "ipaddress" と "ip" の両方が同時に存在することがある (片方が "" や null のことも)。
//     優先順位は ipaddress、空なら ip にフォールバック。
//   - region_id は API レスポンスに存在しない (`regionname` のみ) ため受け取らない。
func (i *Instance) UnmarshalJSON(data []byte) error {
	type apiInstance struct {
		ID             int    `json:"id"`
		InstanceName   string `json:"instance_name"`
		Status         string `json:"status"`         // lifecycle: READY / OPEN
		InstanceStatus string `json:"instancestatus"` // power: Running / Stopped / 遷移中文字列
		OSID           int    `json:"os_id"`
		PlanID         int    `json:"plan_id"`
		IPAddress      string `json:"ipaddress"`
		IP             string `json:"ip"`
		SSHKeyID       int    `json:"sshkey_id"`
	}
	var a apiInstance
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	ipv4 := strings.TrimSpace(a.IPAddress)
	if ipv4 == "" {
		ipv4 = strings.TrimSpace(a.IP)
	}
	i.ID = a.ID
	i.Name = a.InstanceName
	i.LifecycleStatus = a.Status
	i.PowerStatus = a.InstanceStatus
	i.OSID = a.OSID
	i.PlanID = a.PlanID
	i.IPv4 = ipv4
	i.SSHKeyID = a.SSHKeyID
	return nil
}
