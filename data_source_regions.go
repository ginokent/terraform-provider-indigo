package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceRegions() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceRegionsRead,
		Schema: map[string]*schema.Schema{
			"instance_type_id": {
				Type:     schema.TypeInt,
				Optional: true,
				Default:  1,
			},
			"regions": {
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

func dataSourceRegionsRead(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return diag.FromErr(err)
	}
	instanceTypeID := d.Get("instance_type_id").(int)
	regions, err := c.ListRegions(ctx, instanceTypeID)
	if err != nil {
		return diag.FromErr(err)
	}
	items := make([]map[string]any, 0, len(regions))
	for _, region := range regions {
		items = append(items, map[string]any{"id": region.ID, "name": region.Name})
	}

	d.SetId("regions")
	if err := d.Set("regions", items); err != nil {
		return diag.FromErr(err)
	}
	return nil
}
