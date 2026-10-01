---
name: inspect-cloud-services
description: Inspect AWS, Azure, or GCP resources, diagnose deployed services, and investigate cloud costs.
# Named for readers whose profile holds them; other profiles follow the skill without them.
optional_tools:
  - ask_user
metadata:
  paintedwolf.template_resources: references/inspect_aws_resources.md|references/inspect_azure_resources.md|references/inspect_gcp_resources.md|references/debug_a_deployed_cloud_service.md|references/investigate_cloud_costs.md
  host_resources: aws-cli|azure-cli|gcloud-cli
---

# Inspect cloud services

Use the request and observed project/environment to choose the matching procedure below. Read that procedure before acting; load only the variants needed for this task. Resource paths are relative to this skill directory.

- [Inspect aws resources](references/inspect_aws_resources.md) — Answer questions about AWS account state with read-only aws commands when inventorying resources, checking configuration, or capturing cloud evidence.
- [Inspect azure resources](references/inspect_azure_resources.md) — Answer questions about Azure state with read-only az commands when inventorying resources, checking configuration, or capturing Azure evidence.
- [Inspect gcp resources](references/inspect_gcp_resources.md) — Answer questions about Google Cloud state with read-only gcloud commands when inventorying resources, checking configuration, or capturing GCP evidence.
- [Debug a deployed cloud service](references/debug_a_deployed_cloud_service.md) — Diagnose a misbehaving production or staging service from cloud logs and configuration using read-only inspection when the cause is not visible locally.
- [Investigate cloud costs](references/investigate_cloud_costs.md) — Attribute cloud spend to services and changes with read-only billing queries when a bill spikes, budgets drift, or spend needs a breakdown by service or time.

## Boundaries

Follow project policy and the user’s requested scope. A procedure does not grant permission or imply that every listed toolchain is installed. Use only available host resources and tools; delegate or report a missing capability. Preserve original evidence, identify its source, and report verification limits.
