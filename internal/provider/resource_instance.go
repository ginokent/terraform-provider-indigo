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

// provisionTimeout は Indigo の provisioning (status: READY → OPEN) を待つ最大時間。
// 実観測では数分で完了するが、KVM の初回 boot まで含めて 15 分の安全マージンを取る。
const provisionTimeout = 15 * time.Minute

// powerConvergeTimeout は start/stop 発行後に PowerStatus が反映されるまで待つ最大時間。
const powerConvergeTimeout = 5 * time.Minute

// resourceInstance は indigo_instance を表す。
//
// Indigo API は instance に対して 2 系統の状態を返す:
//   - lifecycle (`status`)        : リソース管理面の状態 (READY / OPEN)
//   - power    (`instancestatus`) : VM の電源状態 (Running / Stopped / 遷移中文字列)
//
// これらを Terraform 上でも別フィールドに 1:1 で写し、混同しないようにしている。
//   - `status`          (Computed)         ← lifecycle (lowercased)
//   - `instance_status` (Optional+Computed) ← power の正規化値 (running/stopped)
//   - `status_raw`      (Computed)         ← power の生値 (デバッグ用、遷移中文字列をそのまま見るため)
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
			// status は API の lifecycle 状態 (READY/OPEN を lowercase 化したもの)。
			"status": {
				Type:     schema.TypeString,
				Computed: true,
			},
			// instance_status は power 状態。ユーザは running/stopped のみ指定可。
			// Read 時には API の PowerStatus を normalizePowerStatus で畳んだ値が入る。
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
			// status_raw は API の PowerStatus (instancestatus) を加工せず保持する。
			// 遷移中の "OS installation In Progress" 等を確認するためのデバッグ用フィールド。
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

// resourceInstanceCreate はインスタンス作成と desired power state への収束を行う。
//
// Indigo の挙動:
//  1. createinstance の戻りでは VM はまだ provisioning 中 (lifecycle=READY, power="OS installation In Progress")
//  2. しばらく経つと lifecycle=OPEN になり、ここで power は **必ず Stopped** で停止状態にされる
//     (Indigo 側で provision → 一度起動 → 自動停止 の遷移を行うため)
//  3. ユーザが running を望む場合のみ start を発行する必要がある。stopped は何もしなくてよい
func resourceInstanceCreate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return opDiag("indigo_instance", "create", err)
	}

	inst, err := c.CreateInstance(ctx, createReqFromResource(d))
	if err != nil {
		return opDiag("indigo_instance", "create", err)
	}
	id := inst.ID
	d.SetId(strconv.Itoa(id))

	if err := waitForLifecycleOpen(ctx, c, id, provisionTimeout); err != nil {
		return opDiag("indigo_instance", "create", err)
	}

	desired := normalizePowerStatus(d.Get("instance_status").(string))
	if desired == "running" {
		if err := c.UpdateInstanceStatus(ctx, id, "start"); err != nil && !isIdempotentStatusUpdateError(err, "start") {
			return opDiag("indigo_instance", "create", err)
		}
		if err := waitForPowerStatus(ctx, c, id, "running", powerConvergeTimeout); err != nil {
			return opDiag("indigo_instance", "create", err)
		}
	}

	return resourceInstanceRead(ctx, d, meta)
}

// resourceInstanceRead は API の現在状態をそのまま state に書く。
// instance_status の上書きを desired 値で抑止しないこと:
// 抑止すると drift (例: Indigo 側で勝手に Stopped になった) が terraform plan で検出されなくなる。
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

	_ = d.Set("name", inst.Name)
	_ = d.Set("status", strings.ToLower(strings.TrimSpace(inst.LifecycleStatus)))
	_ = d.Set("instance_status", normalizePowerStatus(inst.PowerStatus))
	_ = d.Set("status_raw", strings.TrimSpace(inst.PowerStatus))
	_ = d.Set("ipv4", inst.IPv4)
	tflog.Debug(ctx, "indigo_instance read", map[string]any{
		"id":               d.Id(),
		"lifecycle_status": strings.TrimSpace(inst.LifecycleStatus),
		"power_status_raw": strings.TrimSpace(inst.PowerStatus),
	})
	// region_id は API レスポンスに含まれない (regionname のみ)。create 時にユーザが
	// 与えた値が state に保持されており、ForceNew なので変更も起きない。ここでは触らない。
	//
	// os_id / plan_id / ssh_key_id は通常返ってくるが、Indigo 側のレース条件で
	// 一時的に 0 が返ることがあるため state を尊重する (perpetual drift 回避)。
	if inst.OSID > 0 {
		_ = d.Set("os_id", inst.OSID)
	}
	if inst.PlanID > 0 {
		_ = d.Set("plan_id", inst.PlanID)
	}
	if inst.SSHKeyID > 0 {
		_ = d.Set("ssh_key_id", inst.SSHKeyID)
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
	if err := waitForPowerStatus(ctx, c, id, after, powerConvergeTimeout); err != nil {
		return opDiag("indigo_instance", "update", err)
	}
	return resourceInstanceRead(ctx, d, meta)
}

// waitForLifecycleOpen は API の lifecycle status が OPEN になるまで待つ。
// READY (provisioning 中) → OPEN (provisioning 完了) の遷移を待ち合わせるためのもの。
func waitForLifecycleOpen(ctx context.Context, c *client.Client, id int, timeout time.Duration) error {
	return retry.RetryContext(ctx, timeout, func() *retry.RetryError {
		inst, err := c.GetInstanceByID(ctx, id)
		if err != nil {
			return retry.RetryableError(err)
		}
		if inst == nil {
			return retry.NonRetryableError(fmt.Errorf("instance %d not found while waiting for OPEN", id))
		}
		ls := strings.ToUpper(strings.TrimSpace(inst.LifecycleStatus))
		if ls == "OPEN" {
			return nil
		}
		return retry.RetryableError(fmt.Errorf("lifecycle status is %q, waiting for OPEN", inst.LifecycleStatus))
	})
}

// waitForPowerStatus は normalizePowerStatus 後の power 状態が want に一致するまで待つ。
// want は "running" / "stopped" のいずれか。
func waitForPowerStatus(ctx context.Context, c *client.Client, id int, want string, timeout time.Duration) error {
	return retry.RetryContext(ctx, timeout, func() *retry.RetryError {
		inst, err := c.GetInstanceByID(ctx, id)
		if err != nil {
			return retry.RetryableError(err)
		}
		if inst == nil {
			return retry.NonRetryableError(fmt.Errorf("instance %d not found while waiting for power=%s", id, want))
		}
		if normalizePowerStatus(inst.PowerStatus) == want {
			return nil
		}
		return retry.RetryableError(fmt.Errorf("power status is %q, waiting for %s", inst.PowerStatus, want))
	})
}

// normalizePowerStatus は Indigo の PowerStatus (instancestatus) を
// Terraform 上の正規表現 "running" / "stopped" に畳み込む。
//
// 実 API で観測されたマッピング元値 (case-insensitive):
//   - "Running" → "running"
//   - "Stopped" → "stopped"
//
// それ以外 (例: "OS installation In Progress" のような遷移中文字列) は
// 既知の power 状態ではないため、lowercased + trimmed の生値をそのまま返す。
// これによりユーザは status_raw / instance_status から遷移中であることを観測できる。
//
// 注意: lifecycle 値 ("READY" / "OPEN" など) を渡してはいけない。
// lifecycle と power は別概念であり、本関数は power 専用。
func normalizePowerStatus(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "running":
		return "running"
	case "stopped":
		return "stopped"
	default:
		return v
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
