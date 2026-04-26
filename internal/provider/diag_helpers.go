package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
)

func opDiag(resourceType, operation string, err error) diag.Diagnostics {
	if err == nil {
		return nil
	}
	return diag.FromErr(fmt.Errorf("%s %s failed: %w", resourceType, operation, err))
}
