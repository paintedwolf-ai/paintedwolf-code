# Debug a failing container

Use this workflow when a container starts and then does not stay up. On a process start that talks to the daemon, declare `docker` or `podman` in `capability_request.host_resources`.

The single idea that explains most of these: **a container lives exactly as long as its main process.** It is not a VM that idles. When PID 1 exits — successfully or not — the container stops. "It exits immediately" is usually the container doing its job correctly on a command that had nothing to keep running.

## Workflow

1. **See the exit code before anything else.** `docker ps -a` shows the status; `docker inspect <c> --format '{% verbatim %}{{.State.ExitCode}} {{.State.OOMKilled}} {{.RestartCount}}{% endverbatim %}'` gives the three facts that decide the whole diagnosis. Do not rebuild before you have them.
2. **Read the code against the table below.** It usually names the cause outright, and it separates "your app failed" from "the container never started your app".
3. **Read the logs**: `docker logs <c>`, and `--since`/`--tail` when it is a restart loop so you get one incarnation rather than a merged blur. A container that died before writing anything is a different problem from one that logged a stack trace.
4. **Get a shell the right way.** `docker exec` needs a *running* container — it cannot help with one that already exited. For a dead container, start a new one over the same image with the entrypoint replaced: `docker run --rm -it --entrypoint sh <image>`. That separates "the image is wrong" from "the command is wrong".
5. **Compare against the host deliberately.** If it works on the host and not in the container, the difference is almost always one of: a missing env var, a path that does not exist inside, a file not copied in, a non-root user without permission, or an architecture mismatch.
6. Capture evidence as the exact command, the exit code, and the relevant log lines. Report what the container did, not what you assume the app intended.

## Exit codes worth knowing

| Code | Meaning |
|------|---------|
| `0` | Main process finished cleanly. The container is *supposed* to stop. Usually a one-shot command where a long-running one was intended. |
| `1` / other low | The application itself errored. Debug the app, not the container. |
| `125` | The daemon failed to run the container — bad flag, bad mount, bad option. Nothing of yours ran. |
| `126` | Command found but not executable — usually a missing execute bit, or CRLF line endings on an entrypoint script. |
| `127` | Command not found — a binary missing from the image, or a shell form assuming a shell a slim/distroless base does not have. |
| `137` | SIGKILL. Check `.State.OOMKilled`, host/cgroup pressure, orchestrator events, and external stop/kill activity before assigning a cause. |
| `139` | SIGSEGV — a native process accessed invalid memory. Capture the native crash; an architecture mismatch more commonly fails at exec with `exec format error`. |
| `143` | SIGTERM — a normal stop. Expected during shutdown. |

## Common causes, in the order worth checking

- **Exits `0` immediately.** The command had nothing to keep alive. A base image with no `CMD` that stays running, or an entrypoint that returns straight away.
- **`137` with `OOMKilled: true`.** The memory limit is too low, or the process genuinely leaks. Watch it with `docker stats` while it runs; raise the limit only after seeing the real usage.
- **Crash loop.** A restart policy is faithfully restarting something that fails every time. Check `RestartCount`, then fix the underlying exit — do not raise the restart limit.
- **Works on host, `127` in container.** A dependency was never installed into the image, or the entrypoint uses `sh` syntax on a base without a shell.
- **Permission denied on a mounted path.** The container user's UID does not own the host directory. This bites on Linux where UIDs map straight through; it is often invisible on macOS, so a "works for me" report may simply be a different host OS.

## Do not

- **Do not add `sleep infinity` or `tail -f /dev/null` to keep it alive** unless you are deliberately holding it open to inspect it, and say so. As a fix it hides the failure rather than resolving it.
- **Do not rebuild the image to "try again"** before reading the exit code. A rebuild changes nothing about a bad mount, a missing env var, or an OOM.
- Do not `docker kill`/`rm` containers you did not start. They may be the user's running services.
- Do not raise a memory limit without first showing the observed usage that justifies it.
