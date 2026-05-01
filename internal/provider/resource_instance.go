package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	"github.com/ginokent/terraform-provider-indigo/internal/client"
)

// resourceInstance は indigo_instance を表す。
//
// status と instance_status を分離している:
//   - status は API レスポンスの生 status を小文字化して載せるだけの読み取り専用フィールド
//   - instance_status は電源状態 (running/stopped) を表すユーザ可変フィールド
//
// Indigo API は単一の "status" に電源状態と API 応答ステータスを混在させてくるため、
// Terraform 上で「ユーザが指示する電源状態」と「API が返す現在状態」を別フィールドに割り当てている。
func resourceInstance() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceInstanceCreate,
		ReadContext:   resourceInstanceRead,
		UpdateContext: resourceInstanceUpdate,
		DeleteContext: resourceInstanceDelete,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"region_id": {
				Type:     schema.TypeInt,
				Required: true,
				ForceNew: true,
			},
			"os_id": {
				Type:     schema.TypeInt,
				Required: true,
				ForceNew: true,
			},
			"plan_id": {
				Type:     schema.TypeInt,
				Required: true,
				ForceNew: true,
			},
			"ssh_key_id": {
				Type:     schema.TypeInt,
				Required: true,
				ForceNew: true,
			},
			"status": {
				Type:     schema.TypeString,
				Computed: true,
			},
			// instance_status は running/stopped のみ受理する。Indigo は他にも多様な
			// 文字列を返すが、ユーザが指定できるのはこの 2 値に限定する (StateFunc で小文字化)。
			"instance_status": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				StateFunc: func(v any) string {
					return strings.ToLower(v.(string))
				},
				ValidateFunc: func(v any, k string) (ws []string, es []error) {
					s := strings.ToLower(v.(string))
					if s != "running" && s != "stopped" {
						es = append(es, fmt.Errorf("%s must be either \"running\" or \"stopped\"", k))
					}
					return ws, es
				},
			},
			"status_raw": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"ipv4": {
				Type:     schema.TypeString,
				Computed: true,
			},
		},
	}
}

func resourceInstanceCreate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return opDiag("indigo_instance", "create", err)
	}

	inst, err := c.CreateInstance(ctx, createReqFromResource(d))
	if err != nil {
		return opDiag("indigo_instance", "create", err)
	}
	d.SetId(strconv.Itoa(inst.ID))

	desiredInstanceStatus := normalizePowerStatus(d.Get("instance_status").(string))
	if desiredInstanceStatus == "stopped" {
		id, _ := strconv.Atoi(d.Id())
		if err := c.UpdateInstanceStatus(ctx, id, "stop"); err != nil && !isIdempotentStatusUpdateError(err, "stop") {
			return opDiag("indigo_instance", "create", err)
		}
	}

	return resourceInstanceRead(ctx, d, meta)
}

func resourceInstanceRead(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return opDiag("indigo_instance", "read", err)
	}

	id, err := strconv.Atoi(d.Id())
	if err != nil {
		return opDiag("indigo_instance", "read", fmt.Errorf("invalid id %s: %w", d.Id(), err))
	}

	inst, err := c.GetInstanceByID(ctx, id)
	if err != nil {
		return opDiag("indigo_instance", "read", err)
	}
	if inst == nil {
		d.SetId("")
		return nil
	}

	desiredInstanceStatus := normalizePowerStatus(d.Get("instance_status").(string))
	resolvedInstanceStatus := normalizePowerStatus(inst.Status)
	_ = d.Set("name", inst.Name)
	_ = d.Set("status", strings.ToLower(strings.TrimSpace(inst.APIStatus)))
	if desiredInstanceStatus == "" {
		_ = d.Set("instance_status", resolvedInstanceStatus)
	}
	_ = d.Set("ipv4", inst.IPv4)
	tflog.Debug(ctx, "resolved instance power status", map[string]any{
		"id":                       d.Id(),
		"desired_instance_status":  desiredInstanceStatus,
		"observed_instance_status": strings.TrimSpace(inst.Status),
		"api_status":               strings.TrimSpace(inst.APIStatus),
		"resolved_instance_status": resolvedInstanceStatus,
	})
	// Indigo API sometimes returns 0 for immutable IDs even when the actual
	// instance was created with non-zero values. Keep existing state values if
	// API returns zero to avoid perpetual drifts caused by upstream inconsistency.
	if inst.RegionID > 0 {
		_ = d.Set("region_id", inst.RegionID)
	}
	if inst.OSID > 0 {
		_ = d.Set("os_id", inst.OSID)
	}
	if inst.PlanID > 0 {
		_ = d.Set("plan_id", inst.PlanID)
	}
	if inst.SSHPublicKey > 0 {
		_ = d.Set("ssh_key_id", inst.SSHPublicKey)
	}
	return nil
}

// resourceInstanceDelete は destroy 後にインスタンスが getinstancelist から消えるまで待つ。
// Indigo の destroy は非同期で、削除 API が成功してもしばらくはリストに残るため、
// 2 分のタイムアウトでポーリングしている。
func resourceInstanceDelete(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return opDiag("indigo_instance", "delete", err)
	}

	id, _ := strconv.Atoi(d.Id())
	if err := c.DeleteInstance(ctx, id); err != nil {
		return opDiag("indigo_instance", "delete", err)
	}

	waitErr := retry.RetryContext(ctx, 2*time.Minute, func() *retry.RetryError {
		inst, err := c.GetInstanceByID(ctx, id)
		if err != nil {
			return retry.RetryableError(err)
		}
		if inst != nil {
			return retry.RetryableError(fmt.Errorf("instance %d still present", id))
		}
		return nil
	})
	if waitErr != nil {
		return opDiag("indigo_instance", "delete", waitErr)
	}

	d.SetId("")
	return nil
}

// resourceInstanceUpdate は instance_status の差分だけを扱う。
// 他のフィールド (name/region_id/os_id/plan_id/ssh_key_id) は ForceNew であり、
// ここに到達した時点で変更されているのは instance_status のみという前提。
func resourceInstanceUpdate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return opDiag("indigo_instance", "update", err)
	}
	if !d.HasChange("instance_status") {
		return resourceInstanceRead(ctx, d, meta)
	}

	id, _ := strconv.Atoi(d.Id())
	beforeRaw, afterRaw := d.GetChange("instance_status")
	before := normalizePowerStatus(fmt.Sprintf("%v", beforeRaw))
	after := normalizePowerStatus(fmt.Sprintf("%v", afterRaw))

	if after == "" || after == before {
		return resourceInstanceRead(ctx, d, meta)
	}

	var command string
	switch after {
	case "running":
		command = "start"
	case "stopped":
		command = "stop"
	default:
		return diag.Errorf("unsupported status %q (allowed: running, stopped)", after)
	}

	if err := c.UpdateInstanceStatus(ctx, id, command); err != nil {
		if isIdempotentStatusUpdateError(err, command) {
			return resourceInstanceRead(ctx, d, meta)
		}
		return opDiag("indigo_instance", "update", err)
	}
	return resourceInstanceRead(ctx, d, meta)
}

// normalizePowerStatus は Indigo が返す多様な電源状態文字列を running/stopped に畳み込む。
//
// マッピング根拠:
//   - running 系: API が start コマンドを受理した直後に "start"、稼働中は "running"/"active"/"ready" を返す
//   - stopped 系: 停止指示後に "stop"/"forcestop"、停止確定後は "stopped"、
//     さらに一部 endpoint では "close"/"closed"/"open" (open は「ポートが開いていない」=停止中の意) を返す
//
// 既知マッピング外の値はそのまま小文字化して返し、上位レイヤで気付けるようにする。
func normalizePowerStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "running", "start", "active", "ready":
		return "running"
	case "stopped", "stop", "forcestop", "close", "closed", "open":
		return "stopped"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// isIdempotentStatusUpdateError は「既に running/stopped」を 400 で返す Indigo の挙動を吸収する。
// 本来冪等であるべき start/stop が "already running"/"already stopped" 等の 400 を返してくるため、
// これらは成功扱いに変換しないと running→running の no-op apply が失敗してしまう。
func isIdempotentStatusUpdateError(err error, command string) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(apiErr.Message + " " + apiErr.Body))
	switch command {
	case "start":
		return strings.Contains(msg, "already running")
	case "stop":
		return strings.Contains(msg, "already stopped") || strings.Contains(msg, "already stop")
	default:
		return false
	}
}

func createReqFromResource(d *schema.ResourceData) client.CreateInstanceRequest {
	return client.CreateInstanceRequest{
		Name:     d.Get("name").(string),
		RegionID: d.Get("region_id").(int),
		OSID:     d.Get("os_id").(int),
		PlanID:   d.Get("plan_id").(int),
		SSHKeyID: d.Get("ssh_key_id").(int),
	}
}
