package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceInstanceSpecs() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceInstanceSpecsRead,
		Schema: map[string]*schema.Schema{
			"instance_type_id": {
				Type:     schema.TypeInt,
				Optional: true,
				Default:  1,
			},
			"os_id": {
				Type:     schema.TypeInt,
				Required: true,
			},
			"instance_specs": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Resource{Schema: map[string]*schema.Schema{
					"id":           {Type: schema.TypeInt, Computed: true},
					"name":         {Type: schema.TypeString, Computed: true},
					"cpu":          {Type: schema.TypeInt, Computed: true},
					"memsize":      {Type: schema.TypeInt, Computed: true},
					"rootdisksize": {Type: schema.TypeInt, Computed: true},
				}},
			},
		},
	}
}

func dataSourceInstanceSpecsRead(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return diag.FromErr(err)
	}
	instanceTypeID := d.Get("instance_type_id").(int)
	osID := d.Get("os_id").(int)
	specs, err := c.ListInstanceSpecs(ctx, instanceTypeID, osID)
	if err != nil {
		return diag.FromErr(err)
	}
	items := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		items = append(items, map[string]any{
			"id":           spec.ID,
			"name":         spec.Name,
			"cpu":          spec.CPU,
			"memsize":      spec.MemSize,
			"rootdisksize": spec.RootDiskSize,
		})
	}
	d.SetId("instance_specs")
	if err := d.Set("instance_specs", items); err != nil {
		return diag.FromErr(err)
	}
	return nil
}
