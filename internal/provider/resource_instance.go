package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	"github.com/example/terraform-provider-webarena-indigo/internal/client"
)

func resourceInstance() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceInstanceCreate,
		ReadContext:   resourceInstanceRead,
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

	_ = d.Set("name", inst.Name)
	_ = d.Set("status", normalizePowerStatus(inst.Status))
	_ = d.Set("ipv4", inst.IPv4)
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

func resourceInstanceUpdate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return opDiag("indigo_instance", "update", err)
	}
	if !d.HasChange("status") {
		return resourceInstanceRead(ctx, d, meta)
	}

	id, _ := strconv.Atoi(d.Id())
	beforeRaw, afterRaw := d.GetChange("status")
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
		return opDiag("indigo_instance", "update", err)
	}
	return resourceInstanceRead(ctx, d, meta)
}

func normalizePowerStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "running", "start":
		return "running"
	case "stopped", "stop", "forcestop":
		return "stopped"
	default:
		return strings.ToLower(strings.TrimSpace(s))
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
