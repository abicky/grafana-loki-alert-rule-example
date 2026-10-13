locals {
  evaluation_interval_seconds = 10
  grouping_labels             = "service_name, deployment_environment_name, error_group, exception_type, severity"
  severity_settings = {
    # Five minutes tolerates delayed delivery. A one-minute window could expire
    # before a buffered FATAL log reaches Loki.
    FATAL = { threshold = 0, query_window = "5m" }
    ERROR = { threshold = 1, query_window = "10m" }
    WARN  = { threshold = 10, query_window = "10m" }
  }
  query_window_seconds = { for severity, settings in local.severity_settings : severity =>
    tonumber(trimsuffix(settings.query_window, "m")) * 60
  }

  # Fallbacks keyed by exception type; log metadata takes precedence.
  runbook_urls = tomap({
    "main.databaseError" = "https://example.com/runbooks/database"
  })
  runbook_url_annotation = <<-TEMPLATE
    {{ $runbook := "" -}}
    {{ with $values.latest_error -}}
      {{ with .Labels.runbook_url -}}
        {{ $runbook = . -}}
      {{ end -}}
    {{ end -}}
    {{ if not $runbook -}}
      %{for exception_type, url in local.runbook_urls~}
        {{ if eq $labels.exception_type ${format("%q", exception_type)} -}}
          {{ $runbook = ${format("%q", url)} -}}
        {{ end -}}
      %{endfor~}
    {{ end -}}
    {{ $runbook -}}
  TEMPLATE

  # The summary includes the latest error's message; prefer firing alerts in mixed groups.
  slack_title = <<-TEMPLATE
    {{ $summary := "No alerts in notification" -}}
    {{ if .Alerts.Firing -}}
      {{ $summary = (index .Alerts.Firing 0).Annotations.summary -}}
    {{ else if .Alerts.Resolved -}}
      {{ $summary = (index .Alerts.Resolved 0).Annotations.summary -}}
    {{ end -}}
    {{ .CommonLabels.service_name }}: {{ $summary -}}
  TEMPLATE

  # Encode the fixed Explore state; insert the query and timestamps at notification time.
  # Remove the query placeholder's quotes because data.ToJSON supplies them.
  explore_panes_format = replace(jsonencode({
    logs = {
      datasource = var.loki_datasource_uid
      queries = [{
        refId      = "logs"
        expr       = "__QUERY__"
        queryType  = "range"
        editorMode = "code"
        datasource = { type = "loki", uid = var.loki_datasource_uid }
      }]
      range = { from = "%s", to = "%s" }
    }
  }), "\"__QUERY__\"", "%s")

  # Display the latest error's original stack trace rather than its normalized fingerprint.
  # Bound variable-length fields because Slack may truncate long notifications;
  # truncation has been observed at roughly 8,000 characters.
  slack_message = <<-TEMPLATE
    {{ range .Alerts -}}
    {{ .Annotations.description }}
      {{ $hasExplore := and .Annotations.explore_query .Annotations.explore_from .Annotations.explore_to -}}
      {{ $hasSilence := and .SilenceURL (le (len .SilenceURL) 4000) -}}
      {{ if or .Annotations.runbook_url .GeneratorURL .DashboardURL $hasExplore $hasSilence }}
    *Actions*

        {{ with .Annotations.runbook_url -}}
    - <{{ . }}|Go to runbook>
        {{ end -}}
        {{ with .GeneratorURL -}}
    - <{{ . }}|Go to alert rule>
        {{ end -}}
        {{ with .DashboardURL -}}
    - <{{ . }}|Go to dashboard>
        {{ end -}}
        {{ if $hasExplore -}}
          {{/* Use data.ToJSON instead of printf "%q", which can emit Go-only escapes such as \x01 that are invalid in JSON. */ -}}
          {{ $panes := printf ${format("%q", local.explore_panes_format)} (data.ToJSON .Annotations.explore_query) .Annotations.explore_from .Annotations.explore_to -}}
    - <${trimsuffix(var.grafana_url, "/")}/explore?schemaVersion=1&panes={{ urlquery $panes }}|Explore logs>
        {{ end -}}
        {{ if $hasSilence -}}
    - <{{ .SilenceURL }}|Create silence>
        {{ end -}}
      {{ end -}}
      {{ with .Annotations.stacktrace }}
    *Stack trace*

    ```
    {{ printf "%.3000s" . }}
    ```
      {{ end }}
      {{ $firstLabel := true -}}
      {{ range $key, $value := .Labels -}}
        {{ if and (ne $key "error_group") (ne $key "service_name") -}}
          {{ if $firstLabel -}}
    *Other labels:*

            {{ $firstLabel = false -}}
          {{ end -}}
    - {{ $key }}: {{ $value }}
        {{ end -}}
      {{ end }}
      {{ $firstAnnotation := true -}}
      {{ range $key, $value := .Annotations -}}
        {{ if and (ne $key "summary") (ne $key "description") (ne $key "runbook_url") (ne $key "stacktrace") (ne $key "explore_query") (ne $key "explore_from") (ne $key "explore_to") -}}
          {{ if $firstAnnotation -}}
    *Other annotations:*

            {{ $firstAnnotation = false -}}
          {{ end -}}
    - {{ $key }}: {{ $value }}
        {{ end -}}
      {{ end }}
    {{ end -}}
  TEMPLATE

  fingerprint_pipeline = <<-LOGQL
    # Exclude errors handled by dedicated metric- or log-based rules from this general rule.
    | alert_general_rule_exclude!="true"
    # errors.Formattable's first two lines and Wraps messages can contain variable error text,
    # such as IP addresses and timestamps.
    # Remove them only from the fingerprint, preserving frame order and line numbers.
    | label_format error_group=`{{ if and .exception_type .exception_stacktrace }}{{ regexReplaceAll "(?m)^Wraps: [^\\r\\n]*(\\r?\\n|$)" (regexReplaceAll "^[^\\r\\n]*\\r?\\n[^\\r\\n]*(\\r?\\n|$)" .exception_stacktrace "") "" }}{{ end }}`
    # Without a usable typed stack, normalize common variable identifiers in the body.
    # Remove UUIDs such as 550e8400-e29b-41d4-a716-446655440000.
    | label_format normalized_message=`{{ regexReplaceAll "(?i)\\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\\b" (__line__) "" }}`
    # Remove IPv6 addresses, including compressed forms.
    | label_format normalized_message=`{{ regexReplaceAll "(?i)[0-9a-f]*:[0-9a-f:]*:[0-9a-f:]*" .normalized_message "" }}`
    # Remove hexadecimal values with a 0x or 0X prefix.
    | label_format normalized_message=`{{ regexReplaceAll "0[xX][0-9a-fA-F]+" .normalized_message "" }}`
    # Remove remaining decimal digits, including those in IPv4 addresses and ports.
    | label_format normalized_message=`{{ regexReplaceAll "[0-9]+" .normalized_message "" }}`
    # Use the normalized body only when no stack-based fingerprint was produced.
    | label_format error_group=`{{ if .error_group }}{{ .error_group }}{{ else }}{{ .normalized_message }}{{ end }}`
  LOGQL

  log_pipelines = { for severity in keys(local.severity_settings) : severity => <<-LOGQL
    {service_name=${format("%q", var.service_name)}, severity_text=${format("%q", severity)}}
    ${chomp(local.fingerprint_pipeline)}
    # Use the normalized severity in both alert identity and notification grouping.
    | label_format severity=`{{ .severity_text }}`
  LOGQL
  }

  count_queries = { for severity, log_pipeline in local.log_pipelines : severity => <<-LOGQL
    sum by (${local.grouping_labels}) (
      count_over_time(
        ${indent(4, chomp(log_pipeline))}
        [${local.severity_settings[severity].query_window}]
      )
    )
  LOGQL
  }

  explore_query_annotation = <<-TEMPLATE
    {{ printf "{service_name=%q, deployment_environment_name=%q}" $labels.service_name $labels.deployment_environment_name }}
  TEMPLATE

  # Select the latest error per group for notification details and the Explore time range.
  latest_error_queries = { for severity, log_pipeline in local.log_pipelines : severity => <<-LOGQL
    topk by (${local.grouping_labels}) (
      1,
      max by (${local.grouping_labels}, error_message, exception_stacktrace, runbook_url, error_time_from, error_time_to) (
        max_over_time(
          ${indent(6, chomp(log_pipeline))}
          # Include the message for notification display, but not in the labels that identify an alert.
          | label_format error_message=`{{ __line__ }}{{ if and .exception_message (not (contains .exception_message (__line__))) }}: {{ .exception_message }}{{ end }}`
          # Save the Explore time range and expose the timestamp as a numeric value for latest-log selection.
          | label_format error_time_from=`{{ unixEpochMillis ((__timestamp__).Add -10000000000) }}`
          | label_format error_time_to=`{{ unixEpochMillis (__timestamp__) }}`
          | label_format error_epoch=`{{ unixEpochMillis (__timestamp__) }}`
          | unwrap error_epoch
          [${local.severity_settings[severity].query_window}]
        )
      )
    )
  LOGQL
  }

  loki_model = {
    datasource    = { type = "loki", uid = var.loki_datasource_uid }
    editorMode    = "code"
    queryType     = "instant"
    instant       = true
    intervalMs    = 1000
    maxDataPoints = 43200
  }

  rules = [for severity in keys(local.severity_settings) : {
    uid            = "demo-errors-${lower(severity)}"
    title          = "Demo application exceptions (${severity})"
    condition      = "condition"
    for            = "0s"
    no_data_state  = "OK"
    exec_err_state = "Error"
    is_paused      = false
    labels         = { demo = "otlp", service_name = var.service_name }
    annotations = {
      summary       = <<-TEMPLATE
        {{ with $labels.exception_type -}}
          {{ . }}
        {{- end -}}
        {{ with $values.latest_error -}}
          {{ with .Labels.error_message -}}
            {{ if $labels.exception_type }}: {{ end }}{{ . }}
          {{- end -}}
        {{- end -}}
      TEMPLATE
      stacktrace    = "{{ with $values.latest_error }}{{ with .Labels.exception_stacktrace }}{{ . }}{{ end }}{{ end }}"
      runbook_url   = local.runbook_url_annotation
      explore_query = trimspace(local.explore_query_annotation)
      explore_from  = "{{ with $values.latest_error }}{{ .Labels.error_time_from }}{{ end }}"
      explore_to    = "{{ with $values.latest_error }}{{ .Labels.error_time_to }}{{ end }}"
      description   = "Environment: {{ $labels.deployment_environment_name }}. {{ $labels.severity }} occurrences in the last ${local.severity_settings[severity].query_window}: {{ $values.count.Value }}. Threshold: > ${local.severity_settings[severity].threshold}."
    }
    data = [
      {
        ref_id              = "count"
        datasource_uid      = var.loki_datasource_uid
        relative_time_range = { from = local.query_window_seconds[severity], to = 0 }
        model               = merge(local.loki_model, { refId = "count", expr = local.count_queries[severity] })
      },
      {
        ref_id              = "latest_error"
        datasource_uid      = var.loki_datasource_uid
        relative_time_range = { from = local.query_window_seconds[severity], to = 0 }
        model               = merge(local.loki_model, { refId = "latest_error", expr = local.latest_error_queries[severity] })
      },
      {
        ref_id              = "latest_count"
        datasource_uid      = "__expr__"
        relative_time_range = { from = 0, to = 0 }
        model = {
          datasource = { type = "__expr__", uid = "__expr__" }
          refId      = "latest_count"
          type       = "reduce"
          expression = "count"
          reducer    = "last"
        }
      },
      {
        ref_id              = "condition"
        datasource_uid      = "__expr__"
        relative_time_range = { from = 0, to = 0 }
        model = {
          datasource = { type = "__expr__", uid = "__expr__" }
          refId      = "condition"
          type       = "math"
          expression = "$latest_count > ${local.severity_settings[severity].threshold}"
        }
      },
    ]
  }]
}
