---
name: run-a-throwaway-service
description: Start an isolated local service or container for a test, query it, and tear it down.
---

# Run a throwaway service

Choose the server version the test expects and give the instance fresh storage.

{% if profile_command_external_exec %}## Private PostgreSQL

Use `private-service -- PROGRAM ARG...` to run a command in this session’s isolated service environment. Files under `/work` persist across service commands. The packaged environment includes PostgreSQL 16. For a fresh instance:

1. Run `private-service -- initdb -D /work/pgdata`.
2. Start `private-service -- postgres -D /work/pgdata -k /work/tmp -c listen_addresses='' &` and keep the job handle.
3. Run `private-service -- pg_isready -h /work/tmp`. Query with `private-service -- psql -h /work/tmp -d postgres -c 'SELECT 42'`.
4. Stop the saved job with `kill %1`, substituting its actual job number. When the session has finished using its private service files, `private-service release` discards them and releases the runtime.

## Container workflow

When the host offers a container engine and the test needs its image, declare the engine id (`docker` or `podman`) in `capability_request.host_resources` on commands that use it.

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
{% else %}This profile can describe the service setup. Starting and querying the instance requires a command profile with external execution.
{% endif %}
