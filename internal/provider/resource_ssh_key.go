package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceSSHKey() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceSSHKeyCreate,
		ReadContext:   resourceSSHKeyRead,
		UpdateContext: resourceSSHKeyUpdate,
		DeleteContext: resourceSSHKeyDelete,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
			},
			"public_key": {
				Type:     schema.TypeString,
				Required: true,
			},
			"status": {
				Type:     schema.TypeString,
				Optional: true,
				Default:  "ACTIVE",
			},
		},
	}
}

func resourceSSHKeyCreate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return diag.FromErr(err)
	}
	key, err := c.CreateSSHKey(ctx, d.Get("name").(string), d.Get("public_key").(string))
	if err != nil {
		return diag.FromErr(err)
	}
	d.SetId(strconv.Itoa(key.ID))
	return resourceSSHKeyRead(ctx, d, meta)
}

func resourceSSHKeyRead(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return diag.FromErr(err)
	}
	id, err := strconv.Atoi(d.Id())
	if err != nil {
		return diag.FromErr(fmt.Errorf("invalid id %s: %w", d.Id(), err))
	}
	key, err := c.GetSSHKeyByID(ctx, id)
	if err != nil {
		return diag.FromErr(err)
	}
	if key == nil {
		d.SetId("")
		return nil
	}
	_ = d.Set("name", key.Name)
	_ = d.Set("public_key", key.PublicKey)
	_ = d.Set("status", key.Status)
	return nil
}

func resourceSSHKeyUpdate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return diag.FromErr(err)
	}
	id, _ := strconv.Atoi(d.Id())
	if !d.HasChanges("name", "public_key", "status") {
		return resourceSSHKeyRead(ctx, d, meta)
	}
	if err := c.UpdateSSHKey(ctx, id, d.Get("name").(string), d.Get("public_key").(string), d.Get("status").(string)); err != nil {
		return diag.FromErr(err)
	}
	return resourceSSHKeyRead(ctx, d, meta)
}

func resourceSSHKeyDelete(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return diag.FromErr(err)
	}
	id, _ := strconv.Atoi(d.Id())
	if err := c.DeleteSSHKey(ctx, id); err != nil {
		return diag.FromErr(err)
	}
	d.SetId("")
	return nil
}
