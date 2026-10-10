terraform {
  required_version = "= 1.16.5"

  required_providers {
    grafana = {
      source  = "grafana/grafana"
      version = "= 4.47.0"
    }
  }
}
