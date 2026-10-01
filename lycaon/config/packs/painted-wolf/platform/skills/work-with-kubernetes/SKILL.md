---
name: work-with-kubernetes
description: Inspect or debug Kubernetes workloads, manage Helm releases, or create an explicitly requested local cluster.
metadata:
  paintedwolf.template_resources: references/inspect_a_kubernetes_cluster.md|references/debug_a_kubernetes_workload.md|references/manage_a_helm_release.md|references/stand_up_a_local_cluster.md
  host_resources: helm|kind|kubectl
---

# Work with kubernetes

Use the request and observed project/environment to choose the matching procedure below. Read that procedure before acting; load only the variants needed for this task. Resource paths are relative to this skill directory.

- [Inspect a kubernetes cluster](references/inspect_a_kubernetes_cluster.md) — Diagnose Kubernetes workloads with read-only kubectl triage when pods crash-loop, rollouts stall, services stop resolving, or cluster state is needed as evidence.
- [Debug a kubernetes workload](references/debug_a_kubernetes_workload.md) — Diagnose an unhealthy Kubernetes workload whose pods are Pending, ImagePullBackOff, CrashLoopBackOff, OOMKilled, never Ready, or otherwise unable to stay up.
- [Manage a helm release](references/manage_a_helm_release.md) — Inspect and change Helm releases with history-first discipline when checking release state, diffing values, or performing a user-requested upgrade or rollback.
- [Stand up a local cluster](references/stand_up_a_local_cluster.md) — Stand up a disposable kind Kubernetes cluster to test charts, operators, or manifests when Kubernetes behavior must be proven without a shared cluster.

## Boundaries

Follow project policy and the user’s requested scope. A procedure does not grant permission or imply that every listed toolchain is installed. Use only available host resources and tools; delegate or report a missing capability. Preserve original evidence, identify its source, and report verification limits.
