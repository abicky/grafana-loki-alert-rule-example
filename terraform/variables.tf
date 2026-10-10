variable "grafana_url" {
  type        = string
  description = "Grafana URL reachable from the host running Terraform."
  default     = "http://localhost:3000"
}

variable "grafana_auth" {
  type        = string
  description = "Grafana API credentials; the provider requires authentication even with anonymous browser access."
  sensitive   = true
  default     = "admin:admin"
}

variable "folder_uid" {
  type        = string
  description = "Alert folder UID."
  default     = "demo-alerts"
}

variable "loki_datasource_uid" {
  type        = string
  description = "UID of the provisioned Loki data source."
  default     = "loki"
}

variable "slack_webhook_url" {
  type        = string
  description = "Slack incoming webhook for demo notifications."
  sensitive   = true
}

variable "service_name" {
  type        = string
  description = "OTLP service.name selected by the general exception rule."
  default     = "otel-log-demo"
}
