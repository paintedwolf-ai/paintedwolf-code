# Connect services in containers

Use this workflow when one container cannot reach another service. On a process start that talks to the daemon, declare `docker` or `podman` in `capability_request.host_resources`.

One misunderstanding causes most of these: **`localhost` inside a container means that container**, not your machine and not the other service. Every container gets its own network namespace and its own loopback. Config copied from a host setup — `localhost:5432`, `127.0.0.1:6379` — is therefore wrong inside a container even though the service is running, and the symptom is `connection refused` on a port that is clearly listening.

## What address to use

| From → to | Address |
|---|---|
| Container → container, same user-defined network | The **service or container name** (`db:5432`). Docker's embedded DNS resolves it. |
| Container → container, default bridge | Name resolution does **not** work here. Put both on a user-defined network — compose does this for you. |
| Container → a service on the host | `host.docker.internal` on macOS and Windows. On **Linux** this does not exist by default — run with `--add-host=host.docker.internal:host-gateway`. |
| Host → container | `localhost:<published>`, and only if the port was **published** with `-p`. |
| Anything → container | `EXPOSE` alone publishes nothing. It is documentation; `-p`/`ports:` is what opens the port. |

## Workflow

1. **Confirm the target is actually listening**, and on which interface. A service bound to `127.0.0.1` inside its container is unreachable from any other container even with correct DNS — it must bind `0.0.0.0` to accept traffic from outside its own namespace. This is a frequent cause of a "correct" setup that still refuses connections.
2. **Check both are on the same user-defined network**: `docker network inspect <net>` lists the containers attached. Compose puts a project's services on one network automatically; containers started separately with `docker run` are not on it.
3. **Test name resolution from inside the source container**, not from the host: `docker exec <c> getent hosts db`. Separates a DNS problem from a connectivity problem in one command.
4. **Then test the port** from the same place — `docker exec <c> nc -z db 5432` or an equivalent. If the name resolves but the port refuses, go back to step 1: it is a bind-address or a not-yet-started service, not networking.
5. **Distinguish "not ready" from "not reachable".** A database container is up long before it accepts connections. An app that starts faster than its dependency sees `connection refused` and often exits. That is a startup-ordering problem — needs a healthcheck and a readiness wait, not a networking fix.
6. Capture evidence as the exact command and its output from inside the container. A test from the host proves nothing about what the container can see.

## Do not

- **Do not hardcode a container IP address.** They are assigned per start and change on the next `up`. The service name is the stable identifier; that is what the embedded DNS is for.
- **Do not reach for `--network=host` to make it work.** It removes network isolation, behaves differently on macOS than Linux, and turns a name-resolution bug into a port-collision bug later.
- Do not publish a port to fix container-to-container traffic. Publishing is for reaching a container from the host; two containers on the same network talk without any published port.
- Do not add a fixed `sleep` to work around startup ordering. Use a healthcheck and depend on it, so the wait is as long as it needs to be and no longer.
