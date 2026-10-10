resource "grafana_folder" "demo" {
  title                        = "OTLP demo"
  uid                          = var.folder_uid
  prevent_destroy_if_not_empty = true
}

resource "grafana_rule_group" "exceptions" {
  name               = "demo-errors"
  folder_uid         = grafana_folder.demo.uid
  interval_seconds   = local.evaluation_interval_seconds
  disable_provenance = true

  dynamic "rule" {
    for_each = local.rules
    content {
      uid            = rule.value.uid
      name           = rule.value.title
      condition      = rule.value.condition
      for            = rule.value.for
      no_data_state  = rule.value.no_data_state
      exec_err_state = rule.value.exec_err_state
      is_paused      = rule.value.is_paused
      labels         = rule.value.labels
      annotations    = rule.value.annotations

      notification_settings {
        contact_point = grafana_contact_point.demo.name
        # Keep notification grouping aligned with the query's aggregation labels.
        group_by        = concat(["alertname", "grafana_folder"], [for label in split(",", local.grouping_labels) : trimspace(label)])
        group_wait      = "10s"
        group_interval  = "30s"
        repeat_interval = "5m"
      }

      dynamic "data" {
        for_each = rule.value.data
        content {
          ref_id         = data.value.ref_id
          datasource_uid = data.value.datasource_uid
          # Grafana returns the API-level query type separately from the model;
          # mirror it to avoid perpetual drift, leaving expression queries unset.
          query_type = try(data.value.model.queryType, null)
          model      = jsonencode(data.value.model)

          relative_time_range {
            from = data.value.relative_time_range.from
            to   = data.value.relative_time_range.to
          }
        }
      }
    }
  }
}

resource "grafana_contact_point" "demo" {
  name               = "otlp-demo-slack"
  disable_provenance = true

  slack {
    url                     = var.slack_webhook_url
    title                   = local.slack_title
    text                    = local.slack_message
    disable_resolve_message = true
  }
}
