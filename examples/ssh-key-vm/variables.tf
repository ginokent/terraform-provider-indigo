variable "ssh_key_name" {
  type        = string
  description = "SSH key name in Indigo"
  default     = "tf-example-key"
}

variable "ssh_public_key" {
  type        = string
  description = "SSH public key contents (e.g. ssh-rsa ... )"
}

variable "instance_name" {
  type        = string
  description = "VM name"
  default     = "tf-indigo-vm"
}

variable "os_id" {
  type        = number
  description = "OS ID"
}

variable "plan_id" {
  type        = number
  description = "Instance plan ID"
}
