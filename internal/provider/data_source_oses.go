package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceOSes() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceOSesRead,
		Schema: map[string]*schema.Schema{
			"instance_type_id": {
				Type:     schema.TypeInt,
				Optional: true,
				Default:  1,
			},
			"oses": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Resource{Schema: map[string]*schema.Schema{
					"id":   {Type: schema.TypeInt, Computed: true},
					"name": {Type: schema.TypeString, Computed: true},
				}},
			},
		},
	}
}

func dataSourceOSesRead(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return diag.FromErr(err)
	}
	instanceTypeID := d.Get("instance_type_id").(int)
	oses, err := c.ListOSes(ctx, instanceTypeID)
	if err != nil {
		return diag.FromErr(err)
	}
	items := make([]map[string]any, 0, len(oses))
	for _, os := range oses {
		items = append(items, map[string]any{"id": os.ID, "name": os.Name})
	}
	d.SetId("oses")
	if err := d.Set("oses", items); err != nil {
		return diag.FromErr(err)
	}
	return nil
}
