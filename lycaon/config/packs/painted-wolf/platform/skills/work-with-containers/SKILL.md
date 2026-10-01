---
name: work-with-containers
description: Containerize apps with a Dockerfile; build Docker images; run, debug, network, test, and publish containers; manage Compose, mounts, resources, and shutdown.
metadata:
  paintedwolf.template_resources: references/author_a_compose_file.md|references/build_a_container_image.md|references/connect_services_in_containers.md|references/debug_a_failing_container.md|references/handle_container_shutdown.md|references/manage_container_data.md|references/publish_a_container_image.md|references/reproduce_in_a_container.md|references/size_container_resources.md|references/test_in_a_container.md
  host_resources: docker|kubectl|podman
optional_tools:
  - chown
---

# Work with containers

Use the request and observed project/environment to choose the matching procedure below. Read that procedure before acting; load only the variants needed for this task. Resource paths are relative to this skill directory.

- [Author a compose file](references/author_a_compose_file.md) — Write or fix Docker Compose files so services start in the right order with correct configuration when adding services or diagnosing startup and environment problems.
- [Build a container image](references/build_a_container_image.md) — Write or fix a Dockerfile so the build succeeds, caches well, and produces a small image when a build fails, takes too long, ships a huge image, or a Dockerfile needs writing.
- [Connect services in containers](references/connect_services_in_containers.md) — Fix container connectivity to another service—a database, API, or host—when inter-container connections, published ports, or host-working code fail inside the container.
- [Debug a failing container](references/debug_a_failing_container.md) — Diagnose a container that exits immediately, crash-loops, gets OOM-killed, or differs from the host when a container will not stay up, dies quietly, or restarts forever.
- [Handle container shutdown](references/handle_container_shutdown.md) — Fix a container that ignores stop signals, burns its stop timeout, or drops work on shutdown when docker stop waits until forced, rollouts are slow, or stop exits 137.
- [Manage container data](references/manage_container_data.md) — Decide where container data lives and fix mounts that lose data, shadow files, or deny permission when data vanishes after recreation or a bind mount hides or blocks writes.
- [Publish a container image](references/publish_a_container_image.md) — Tag and push a container image to a registry, safely and reproducibly when an image must reach a registry for CI, a deploy, or a teammate.
- [Reproduce in a container](references/reproduce_in_a_container.md) — Reproduce a failure or verify a change inside a clean, pinned container when a bug looks environment-specific, CI disagrees with local, or clean-checkout must be proven.
- [Size container resources](references/size_container_resources.md) — Set CPU and memory requests and limits from measured usage when a container is OOMKilled, throttled, evicted, or scheduled onto a node that cannot hold it.
- [Test in a container](references/test_in_a_container.md) — Run project verification or tests in a container when the project defines one or the host lacks its toolchain, not to route around a sandbox refusal.

## Boundaries

Follow project policy and the user’s requested scope. A procedure does not grant permission or imply that every listed toolchain is installed. Use only available host resources and tools; delegate or report a missing capability. Preserve original evidence, identify its source, and report verification limits.
