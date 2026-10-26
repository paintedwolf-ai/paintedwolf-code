# Manage container data

Use this workflow for anything about where container data lives. On a process start that touches volumes or mounts, declare `docker` or `podman` in `capability_request.host_resources`.

The rule everything follows: **a container's writable layer belongs to that container.** It survives an ordinary stop/start or restart, but disappears when the container is removed and recreated. Anything that must outlive the individual container belongs on a volume or bind mount.

## Three kinds of storage, and when each is right

| Kind | Lives where | Survives removal | Use for |
|---|---|---|---|
| **Writable layer** | Inside one container | Until that container is removed | Scratch and disposable state only |
| **Named volume** | Managed by the engine | Yes | Databases, caches, anything the container stores. |
| **Bind mount** | A host path you choose | Yes (it is your directory) | Source code in development, config you edit. |

## The four things that actually go wrong

**A bind mount shadows what the image installed.** Mounting your project over `/app` hides everything the build put there — most famously `node_modules`, but equally `target/`, `vendor/`, `.venv`. The directory is replaced, not merged. The fix is an anonymous volume on the inner path (`/app/node_modules`), because the more specific mount wins and the image's content shows through.

**Docker normally copies image content into a new empty volume; a bind mount does not.** Mount a new empty Docker volume over a non-empty image directory and Docker propagates the existing directory into the volume unless `volume-nocopy` is selected. A bind mount shows the host directory exactly as mounted. Confirm the engine's behavior rather than assuming portability, and remember that a volume populated once keeps that content after the image changes.

**Permission denied on a bind mount.** The container's user UID does not own the host directory. On Linux the UID maps straight through, so a container running as UID 1000 writing to a root-owned directory fails. On macOS and Windows the VM translates ownership, which is why this reproduces for one colleague and not another. Fixes, in order of preference: use a named volume and let the engine own it; run with a matching `--user`; or `chown` in an entrypoint. Do not solve it by running as root.

**Data deleted by a cleanup command.** `docker compose down -v` removes the project's named volumes — the `-v` is the whole difference between stopping a stack and destroying its databases. `docker volume prune` and `system prune --volumes` do the same more broadly.

## Workflow

1. **Establish what is mounted where** before theorising: `docker inspect <c>` shows the `Mounts` array with type, source, and destination. `docker volume ls` and `volume inspect` show what exists.
2. **Match the symptom to the four cases above.** "Files are missing" is usually shadowing; "data is gone after recreation" points to the old writable layer or a volume cleanup; data gone after a true restart needs a different explanation because the same writable layer remains attached.
3. **Verify from inside**: `docker exec <c> ls -la <path>` shows what the container actually sees and who owns it — which is frequently not what the host shows.
4. **When data must be preserved, copy it out before changing anything**: `docker cp`, or `tar` from a helper container mounting the volume read-only. Take the copy first; a mount fix can involve recreating the container.

## Do not

- **Do not run `docker compose down -v`, `volume prune`, or `system prune --volumes` without explicit confirmation.** These delete data the user may have nowhere else. Say exactly which volumes would go and let them decide.
- **Do not remove a volume to "reset" state** unless asked. Recreate a container instead; the volume is the durable part.
- **Do not run the container as root to fix a permission error.** It works, and it makes every file the container writes root-owned on the host — the user then cannot edit their own files.
- Do not put database data on a bind mount into a macOS or Windows host path expecting normal performance or locking; that path crosses the VM boundary and both suffer.
- Do not assume the host's view of a mounted directory matches the container's. Check from inside.
