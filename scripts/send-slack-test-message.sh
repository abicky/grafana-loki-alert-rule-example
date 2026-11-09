#!/usr/bin/env bash
set -euo pipefail

executable="$(basename "$0")"
usage() {
  cat <<USAGE
Usage: $executable MESSAGE_FILE
Send a UTF-8 text file to SLACK_WEBHOOK_URL using Grafana-style attachments.
USAGE
}

if [[ "${1:-}" == --help || "${1:-}" == -h ]]; then
  usage
  exit 0
fi
if [[ $# -ne 1 ]]; then
  usage >&2
  exit 1
fi

message_file="$1"
if [[ ! -f "$message_file" || ! -r "$message_file" ]]; then
  printf '%s: message file is not a readable regular file: %s\n' "$executable" "$message_file" >&2
  exit 2
fi
if [[ -z "${SLACK_WEBHOOK_URL:-}" ]]; then
  printf '%s: set SLACK_WEBHOOK_URL before sending\n' "$executable" >&2
  exit 2
fi

grafana_url="${TF_VAR_grafana_url:-http://localhost:3000}"

# Match the incoming-webhook attachment and headers used by Grafana 13.2.3.
# https://github.com/grafana/alerting/blob/e1a167a201a8/receivers/slack/v1/slack.go#L315-L335
# https://github.com/grafana/alerting/blob/e1a167a201a8/receivers/slack/v1/slack.go#L387-L410
payload="$(jq -n --rawfile text "$message_file" \
  --arg title_link "${grafana_url%/}/alerting/list" \
  --argjson timestamp "$(date +%s)" '{
    username: "Grafana",
    attachments: [
      {
        title: "Slack template test",
        title_link: $title_link,
        text: $text,
        fallback: "Slack template test",
        footer: "Grafana v13.2.3",
        footer_icon: "https://grafana.com/static/assets/img/fav32.png",
        color: "#D63232",
        ts: $timestamp
      }
    ]
  }')"

curl --fail-with-body --no-progress-meter --max-time 30 \
  --header 'Content-Type: application/json; charset=utf-8' \
  --user-agent Grafana \
  --data-binary "$payload" \
  -- "$SLACK_WEBHOOK_URL"
printf '\n'
