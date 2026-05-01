package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	"github.com/ginokent/terraform-provider-indigo/internal/client"
)

func New() *schema.Provider {
	p := &schema.Provider{
		Schema: map[string]*schema.Schema{
			"api_key": {
				Type:        schema.TypeString,
				Optional:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("WEBARENA_INDIGO_API_KEY", nil),
				Description: "Indigo API key. Falls back to env WEBARENA_INDIGO_API_KEY.",
			},
			"api_secret": {
				Type:        schema.TypeString,
				Optional:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("WEBARENA_INDIGO_API_SECRET", nil),
				Description: "Indigo API secret. Falls back to env WEBARENA_INDIGO_API_SECRET.",
			},
			"oauth_endpoint": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("WEBARENA_INDIGO_OAUTH_ENDPOINT", nil),
				Description: "OAuth token endpoint. Defaults to https://api.customer.jp/oauth/v1. Env: WEBARENA_INDIGO_OAUTH_ENDPOINT.",
			},
			"indigo_endpoint": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("WEBARENA_INDIGO_ENDPOINT", nil),
				Description: "Indigo API base endpoint. Defaults to https://api.customer.jp/webarenaIndigo/v1. Env: WEBARENA_INDIGO_ENDPOINT.",
			},
		},
		ResourcesMap: map[string]*schema.Resource{
			"indigo_instance": resourceInstance(),
			"indigo_ssh_key":  resourceSSHKey(),
		},
		DataSourcesMap: map[string]*schema.Resource{
			"indigo_instance_types": dataSourceInstanceTypes(),
			"indigo_regions":        dataSourceRegions(),
			"indigo_oses":           dataSourceOSes(),
			"indigo_instance_specs": dataSourceInstanceSpecs(),
		},
		ConfigureContextFunc: configure,
	}
	return p
}

func configure(ctx context.Context, d *schema.ResourceData) (any, diag.Diagnostics) {
	cfg := client.Config{
		APIKey:         d.Get("api_key").(string),
		APISecret:      d.Get("api_secret").(string),
		OAuthEndpoint:  d.Get("oauth_endpoint").(string),
		IndigoEndpoint: d.Get("indigo_endpoint").(string),
	}

	if cfg.APIKey == "" || cfg.APISecret == "" {
		return nil, diag.Errorf("api_key and api_secret are required")
	}

	c := client.New(cfg)
	if _, err := c.ListRegions(ctx, 1); err != nil {
		tflog.Warn(ctx, "provider configured but initial API probe failed", map[string]any{"error": err.Error()})
		if apiErr, ok := err.(*client.APIError); ok && apiErr.StatusCode == 401 {
			return nil, diag.Errorf("invalid API credentials: %s", apiErr.Error())
		}
		return c, nil
	}

	tflog.Info(ctx, "webarena indigo provider configured")
	return c, nil
}

func apiClient(meta any) (*client.Client, error) {
	c, ok := meta.(*client.Client)
	if !ok {
		return nil, fmt.Errorf("unexpected provider client type: %T", meta)
	}
	return c, nil
}
