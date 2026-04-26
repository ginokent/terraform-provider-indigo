package provider

import (
	"context"
	"fmt"
	"strconv"
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
		UpdateContext: resourceInstanceUpdate,
		DeleteContext: resourceInstanceDelete,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
			},
			"region_id": {
				Type:     schema.TypeInt,
				Required: true,
			},
			"os_id": {
				Type:     schema.TypeInt,
				Required: true,
			},
			"plan_id": {
				Type:     schema.TypeInt,
				Required: true,
			},
			"ssh_key_id": {
				Type:     schema.TypeInt,
				Required: true,
			},
			"status": {
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
	_ = d.Set("status", inst.Status)
	_ = d.Set("ipv4", inst.IPv4)
	_ = d.Set("region_id", inst.RegionID)
	_ = d.Set("os_id", inst.OSID)
	_ = d.Set("plan_id", inst.PlanID)
	_ = d.Set("ssh_key_id", inst.SSHPublicKey)
	return nil
}

func resourceInstanceUpdate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return opDiag("indigo_instance", "update", err)
	}

	id, _ := strconv.Atoi(d.Id())
	if d.HasChange("name") || d.HasChange("region_id") || d.HasChange("os_id") || d.HasChange("plan_id") || d.HasChange("ssh_key_id") {
		// Indigo API has no update endpoint for mutable fields. Force replacement behavior.
		return diag.Errorf("instance attributes are immutable in Indigo API; recreate the resource")
	}
	if d.HasChange("status") {
		diags := diag.Diagnostics{}
		n := d.Get("status").(string)
		switch n {
		case "start", "stop", "forcestop", "reset":
			err = c.UpdateInstanceStatus(ctx, id, n)
		default:
			return diag.Errorf("unsupported status transition %q (allowed: start, stop, forcestop, reset)", n)
		}
		if err != nil {
			diags = append(diags, opDiag("indigo_instance", "update", err)...)
		}
		return diags
	}
	return resourceInstanceRead(ctx, d, meta)
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

func createReqFromResource(d *schema.ResourceData) client.CreateInstanceRequest {
	return client.CreateInstanceRequest{
		Name:     d.Get("name").(string),
		RegionID: d.Get("region_id").(int),
		OSID:     d.Get("os_id").(int),
		PlanID:   d.Get("plan_id").(int),
		SSHKeyID: d.Get("ssh_key_id").(int),
	}
}
