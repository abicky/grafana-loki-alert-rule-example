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

variable "runbook_fallback_rules" {
  type = list(object({
    label    = string
    operator = string
    value    = string
    url      = string
  }))
  description = "Ordered fallback rules matched against the latest error's labels after checking its explicit runbook_url."
  default = [
    {
      label    = "exception_type"
      operator = "eq"
      value    = "main.databaseError"
      url      = "https://example.com/runbooks/database"
    },
    {
      label    = "exception_message"
      operator = "match"
      value    = "configuration is unavailable"
      url      = "https://example.com/runbooks/configuration"
    },
  ]

  validation {
    condition     = alltrue([for rule in var.runbook_fallback_rules : contains(["eq", "ne", "match"], rule.operator)])
    error_message = "Fallback operators must be eq, ne, or match."
  }

  validation {
    condition     = alltrue([for rule in var.runbook_fallback_rules : can(regex("^[a-zA-Z_][a-zA-Z0-9_]*$", rule.label))])
    error_message = "Fallback labels must be valid Loki label names (use underscores for OTLP attribute dots)."
  }

  validation {
    condition     = alltrue([for rule in var.runbook_fallback_rules : rule.operator != "match" || can(regexall(rule.value, ""))])
    error_message = "Match fallback values must be valid regular expressions."
  }
}
