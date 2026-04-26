package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceInstanceTypes() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceInstanceTypesRead,
		Schema: map[string]*schema.Schema{
			"instance_types": {
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

func dataSourceInstanceTypesRead(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	c, err := apiClient(meta)
	if err != nil {
		return opDiag("indigo_instance_types", "read", err)
	}
	types, err := c.ListInstanceTypes(ctx)
	if err != nil {
		return opDiag("indigo_instance_types", "read", err)
	}
	items := make([]map[string]any, 0, len(types))
	for _, it := range types {
		items = append(items, map[string]any{"id": it.ID, "name": it.Name})
	}
	d.SetId("instance_types")
	if err := d.Set("instance_types", items); err != nil {
		return opDiag("indigo_instance_types", "read", err)
	}
	return nil
}
