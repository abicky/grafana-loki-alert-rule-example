# Contact points with secrets managed outside Terraform

Configure the Slack webhook once in Grafana, then use
`grafana_apps_generic_resource` to manage the contact point's non-secret
settings. Terraform keeps the integration UID and `secureFields.url = true`,
which tells Grafana to reuse the stored webhook. The webhook itself is not a
Terraform input and is not returned to the provider.

This setup was checked with Grafana 13.2.3, Terraform 1.16.5, and Grafana
provider 4.47.0. The [generic resource is experimental](https://github.com/grafana/terraform-provider-grafana/blob/v4.47.0/docs/resources/apps_generic_resource.md).
The default configuration
in `terraform/main.tf` still uses `grafana_contact_point` and stores its webhook
input in state; the configuration below is an alternative for a new setup.

## Set the webhook once in Grafana

1. Open Alerting > Contact points in Grafana and create a contact point named
   `otlp-demo-slack` with one Slack integration.
2. Enter the real incoming webhook URL and save the contact point. This is the
   only step that needs the webhook value.
3. Read the Receiver through the App Platform API to obtain its identifiers and
   confirm that Grafana reports the stored secret as a marker:

   ```bash
   curl --fail-with-body --no-progress-meter \
     --user admin:admin \
     http://localhost:3000/apis/notifications.alerting.grafana.app/v0alpha1/namespaces/default/receivers |
     jq '.items[] | select(.spec.title == "otlp-demo-slack") | {
       receiver_name: .metadata.name,
       slack_integration_uid: .spec.integrations[0].uid,
       version: .spec.integrations[0].version,
       settings: .spec.integrations[0].settings,
       secureFields: .spec.integrations[0].secureFields
     }'
   ```

   The URL and credentials above are the local demo defaults. Use the namespace
   and credentials for the Grafana instance being configured. In the default
   organization of this self-hosted setup, the API namespace is `default`.

   The response should contain `secureFields: {"url": true}` and no real
   webhook URL in `settings`. This is a normal, redacted read; do not use a
   decrypted contact-point export as Terraform input.

Keep both identifiers: the Receiver's `metadata.name` identifies the contact
point, while the integration's `uid` identifies the Slack integration whose
secret must be preserved. Neither is the Receiver's `metadata.uid` UUID.

Grafana [rejects a caller-supplied `metadata.name`](https://github.com/grafana/grafana/blob/v13.2.3/pkg/registry/apps/alerting/notifications/receiver/legacy_storage.go)
when creating a Receiver,
while the generic Terraform resource requires an object name. Creating the
Receiver outside Terraform and importing it avoids this creation mismatch.
An empty Receiver can also be created outside Terraform, but its Slack
integration and real webhook must be configured before applying the
secret-preserving manifest below.

## Manage the non-secret configuration

Use the repository's provider configuration and `grafana_message_template.slack`
resources with the following contact-point definition. This configuration has
no `slack_webhook_url` variable and does not use `TF_VAR_slack_webhook_url`.
Only the public identifiers obtained above are supplied to Terraform.

```hcl
variable "slack_receiver_name" {
  type        = string
  description = "Existing Grafana Receiver metadata.name."
}

variable "slack_integration_uid" {
  type        = string
  description = "Existing Slack integration UID within the Receiver."
}

resource "grafana_apps_generic_resource" "demo" {
  manifest = {
    apiVersion = "notifications.alerting.grafana.app/v0alpha1"
    kind       = "Receiver"
    metadata = {
      name = var.slack_receiver_name
    }
    spec = {
      title = "otlp-demo-slack"
      integrations = [{
        uid                   = var.slack_integration_uid
        type                  = "slack"
        version               = "v1"
        disableResolveMessage = true
        settings = {
          title = format("{{ template %q . }}", grafana_message_template.slack["title"].name)
          text  = format("{{ template %q . }}", grafana_message_template.slack["message"].name)
        }
        secureFields = {
          url = true
        }
      }]
    }
  }
}
```

The example assumes one Slack `v1` integration. Match the integration version
and include any other non-secret settings needed by the contact point. Keep
the same title, Receiver name, and integration UID when adopting this setup.
Use this resource as the contact-point definition rather than declaring a
`grafana_contact_point` for the same object.

The alert rule refers to the contact point's display title:

```hcl
notification_settings {
  contact_point = grafana_apps_generic_resource.demo.manifest.spec.title
  # Keep the existing grouping and timing arguments here.
}
```

Set the two identifier variables from the redacted response, then import the
existing Receiver before applying the configuration:

```bash
export TF_VAR_slack_receiver_name='<receiver_name from the API response>'
export TF_VAR_slack_integration_uid='<slack_integration_uid from the API response>'
terraform -chdir=terraform import grafana_apps_generic_resource.demo \
  "notifications.alerting.grafana.app/v0alpha1/Receiver/${TF_VAR_slack_receiver_name}"
terraform -chdir=terraform plan
terraform -chdir=terraform apply
```

The import identifier ends with the Receiver's `metadata.name`, not its display
title or UUID. Review the plan: it should update non-secret settings without
creating a Receiver or supplying a webhook URL. Subsequent changes to the
notification title, message, or resolve-message setting use the stored webhook
without setting it again.

## Why a marker is sufficient

`secureFields.url = true` is a non-secret retention marker, not the webhook
value. Grafana uses the existing integration UID to copy that integration's
stored secret into an update. Its normal Receiver response returns the marker
instead of the secret. See the
[Receiver conversion](https://github.com/grafana/grafana/blob/v13.2.3/pkg/registry/apps/alerting/notifications/receiver/conversions.go)
and [secure-field preservation](https://github.com/grafana/grafana/blob/v13.2.3/pkg/services/ngalert/models/receivers.go)
implementations.

Leave `settings.url` out entirely. A literal dummy URL is unnecessary: with
provider 4.47.0, Grafana removes that secret field from the read response, so
Terraform repeatedly plans to add the dummy value. Adding
`ignore_changes = [manifest.spec.integrations[0].settings.url]` did not eliminate
this diff in the checked configuration. The marker-only configuration produced
a clean subsequent plan.

Do not replace the marker with a reference to the actual webhook, or set the
generic resource's top-level `secure` argument to that webhook. This workflow
uses Grafana's already stored integration secret, rather than passing a secret
through Terraform.

Deleting the Receiver or replacing its Slack integration loses the stored
secret association and requires configuring the webhook outside Terraform
again. Keep the integration UID and retention marker intact during ordinary
updates. This approach does not erase secrets from state files or historical
state versions created by a different configuration.

## Verification

In a disposable Grafana instance, a synthetic webhook was configured through
the API outside Terraform, followed by import and two Terraform updates to
non-secret settings. A decrypted API export was used only by that isolated
check to confirm that the webhook remained unchanged. Neither the current
Terraform state nor its backup contained the synthetic webhook, and the final
plan reported no changes. No Slack notification was sent during verification.
