terraform {
  required_providers {
    indigo = {
      source  = "local/indigo"
      version = "0.1.0"
    }
  }
}

provider "indigo" {
  # APIキーは環境変数でも設定できます
  # WEBARENA_INDIGO_API_KEY
  # WEBARENA_INDIGO_API_SECRET
}

resource "indigo_ssh_key" "this" {
  name       = var.ssh_key_name
  public_key = var.ssh_public_key
}

resource "indigo_instance" "vm" {
  name       = var.instance_name
  region_id  = 1
  os_id      = var.os_id
  plan_id    = var.plan_id
  ssh_key_id = tonumber(indigo_ssh_key.this.id)
  instance_status = "RUNNING" # "RUNNING" or "STOPPED"

  depends_on = [indigo_ssh_key.this]
}

output "ssh_key_id" {
  value = indigo_ssh_key.this.id
}

output "instance_id" {
  value = indigo_instance.vm.id
}

output "instance_ipv4" {
  value = indigo_instance.vm.ipv4
}
