# Author a Compose file

Use this workflow to write or repair a compose file. On a process start that runs compose, declare `docker` or `podman` in `capability_request.host_resources`.

For *running* an existing stack, `run-the-project-stack` is the skill. This one is about the file.

## The ordering trap

`depends_on` in its plain form waits for the dependency's container to **start**, not to become usable. A database container is "started" many seconds before it accepts connections, so an app that starts faster than its database sees `connection refused` and often exits — and the compose file looks correct.

Wait on readiness instead, which needs two halves — a healthcheck on the dependency and a condition on the dependent:

```yaml
services:
  db:
    image: postgres:16
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      timeout: 3s
      retries: 10
      start_period: 30s     # grace before failures count, for slow first boot
  app:
    depends_on:
      db:
        condition: service_healthy
```

A fixed `sleep` in the app's entrypoint is the alternative people reach for; it is always either too short on a slow machine or wasted time on a fast one.

## Environment variable precedence

Keep interpolation separate from the environment passed into a container. The host shell and Compose's `.env` / `--env-file` sources supply values for `${VAR}` interpolation; they do not put a variable in the container by themselves.

For the final container environment, the important precedence is:

1. `docker compose run -e` on the CLI
2. An `environment:` or `env_file:` value interpolated from the host shell or a Compose environment file
3. A literal `environment:` value in the service
4. A literal service `env_file:` value
5. `ENV` from the image

Use `docker compose config --environment` to inspect interpolation inputs and `docker compose config` to inspect the resolved model. An exported shell variable overrides `.env` only when the Compose model interpolates it; it does not automatically override a literal service `environment:` entry.

## Writing the rest of it

- **Ports** are `"host:container"`. Only `ports:` publishes; `expose:` documents and publishes nothing. Container-to-container traffic needs no published port at all.
- **Service names are hostnames.** Compose puts a project's services on one network where they resolve each other by service name — see `references/connect_services_in_containers.md`.
- **Declare named volumes at the top level** and reference them by name. Anything else lives in the ephemeral writable layer; see `references/manage_container_data.md`.
- **Use `profiles:`** for services that should not start by default — seeders, admin tools, optional dependencies — rather than commenting blocks in and out.
- **Pin image tags.** `latest` in a compose file means two developers run different software and neither knows.
- **`restart:`** belongs on services that should survive a daemon restart; do not put it on one-shot jobs, which will loop.

## Do not

- **Do not put secrets in the compose file.** It is committed. Use `env_file:` for an ignored file, or the platform's secret mechanism, and never a literal password in `environment:`.
- **Do not use `links:`** — it is legacy and the network already provides name resolution.
- **Do not add `sleep` to entrypoints for ordering.** Use a healthcheck and a condition.
- Do not set `container_name:` on a service you may scale; it makes more than one replica impossible.
- Do not add `network_mode: host` to make connectivity work — it removes isolation and behaves differently on macOS than Linux.
