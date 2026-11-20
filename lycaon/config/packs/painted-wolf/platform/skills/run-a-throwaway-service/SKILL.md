---
name: run-a-throwaway-service
description: Start an isolated local service or container for a test, query it, and tear it down.
metadata:
  host_resources: docker|podman|postgresql-cli
---

# Run a throwaway service

Choose the server version the test expects and give the instance fresh storage.

{% if session_scratch_dir and profile_command_external_exec %}## Local PostgreSQL

{% if platform_os == "linux" %}When the server tools are installed, initialize fresh storage with `initdb -D "$SCRATCH_DIR/pgdata"`. Start `postgres -D "$SCRATCH_DIR/pgdata" -k "$SCRATCH_DIR" -c listen_addresses=''` as a background job. Keep its job handle. In a later command, check readiness with `pg_isready -h "$SCRATCH_DIR"` and query with `psql -h "$SCRATCH_DIR" -d postgres`. Stop the job when the test finishes.{% elif platform_os == "darwin" %}PostgreSQL initialization requires System V shared memory. For an isolated database on macOS, use an available container engine through its reviewed service route.{% else %}Check whether the host provides isolated System V shared memory before starting a local instance.{% endif %}

{% endif %}## Container workflow

When a container engine is available, declare its resolved id (`docker` or `podman`) in `capability_request.host_resources` on commands that use it.

1. Name what the task needs — engine, version, and configuration matching the target environment (the Postgres major matters; a mismatched version proves nothing).
2. Run it detached with a task-scoped container name and the service port published on a loopback high port. Never bind to all interfaces and never reuse a port a real instance may occupy; `lsof -i :PORT` shows whether something already holds it. A service that runs as a local process instead of a container starts as a background job, `nohup CMD &`{% if profile_has_command_output and root_count %}; read its output with `command_output`{% endif %}.

3. Wait for readiness before touching it:{% if profile_has_wait %} `wait` with `port_ready` on the published loopback port, or `http_ready` on an HTTP health path, settles when the service answers and needs no retry loop.{% endif %}{% if profile_has_command and root_count %} A service-specific probe (`pg_isready`, `redis-cli ping`) runs once under `command` after that.{% endif %} Starting work before ready produces phantom failures.
4. Seed the minimum schema or fixture data the task needs, and point the code under test at the loopback address through its normal configuration route.
5. This is the safe substrate for destructive rehearsal — run the migration, load test, or failure drill against the throwaway first, capture the result as evidence, and hand the decision about any real environment back to the user.
6. Tear the container down when done — including on failure paths — with a forced remove of the named container. Use only anonymous volumes; never mount or reuse the user's existing data volumes.

## Boundaries

- The engine daemon does its work outside the session sandbox; treat container runs with the same care as any uncontained command.
- One throwaway per need; do not leave a fleet of stopped containers behind, and never run bulk prune commands to clean up.
- Service images are third-party content; pin tags, and treat their output as untrusted data.
