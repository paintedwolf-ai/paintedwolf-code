# Handle container shutdown

Use this workflow when a container does not stop cleanly. On a process start that runs or stops containers, declare `docker` or `podman` in `capability_request.host_resources`.

Stopping a container is: send the configured stop signal to PID 1, wait for the engine's configured timeout, then send SIGKILL. Docker's Linux default is commonly 10 seconds, but Compose, the engine, and the operating system can change it; inspect the actual configuration instead of assuming a number. PID 1 also has special Unix signal and child-reaping behavior, so a process that relies only on a default terminating action may not behave as it did outside a container.

The tell is unmistakable: every stop takes *exactly* the grace period, and the container exits `137`.

## The two causes

**1. Your process is not PID 1.** Shell form — `CMD npm start` — runs as `/bin/sh -c "npm start"`. The shell is PID 1, it receives the SIGTERM, and it does not forward anything to its child. Your application never learns it should stop.

Use exec form so your process *is* PID 1:

```dockerfile
CMD ["npm", "start"]        # exec form — your process is PID 1
CMD npm start               # shell form — sh is PID 1, signals stop there
```

In an entrypoint script, end with `exec "$@"` (or `exec <your-command>`) so the final process **replaces** the shell rather than running as its child.

**2. Your process is PID 1 but does not handle the stop signal usefully.** Install an explicit handler that stops accepting new work, drains what is in flight, and exits. Check the runtime and framework before changing code; many already install handlers, and the real problem may be a wrapper swallowing the signal or a drain that legitimately needs longer.

## Workflow

1. **Confirm the signature.** Inspect the configured stop signal and timeout, then time a `docker stop`. Consistently consuming the full timeout followed by `137` proves forced termination; it does not yet distinguish missing signal delivery from a handler whose drain is too slow.
2. **Check the form of `CMD`/`ENTRYPOINT` first.** It is the more common of the two causes and the cheaper to fix. `docker inspect` shows the resolved form.
3. **Check whether the process handles SIGTERM** if it is genuinely PID 1. Send it directly (`docker kill -s TERM <c>`) and watch whether anything happens.
4. **Use `--init` (or tini) when the process legitimately cannot be PID 1** — for example when you truly need a wrapper script running alongside. It forwards signals and reaps zombies, which is the other PID 1 duty most applications do not perform. Orphaned processes reparent to PID 1, and a PID 1 that never `wait()`s leaks them as zombies until the table fills.
5. **Set `STOPSIGNAL` if the application expects something other than SIGTERM** — nginx wants SIGQUIT for a graceful stop, and will otherwise shut down hard.
6. **Verify by measuring again.** Confirm the process receives the intended signal, finishes its documented drain within the configured budget, and is not force-killed. Report the observed exit status; applications that translate a handled signal to a nonzero status may need their own convention fixed separately.

## Why this is worth fixing

The visible cost is a slow stop. The real cost is what happens during those ten seconds and the SIGKILL that ends them: in-flight requests dropped rather than drained, buffered writes never flushed, connections left hanging, and in the worst case data written half-way. On Kubernetes every rolling update pays the full grace period per pod, turning a deploy into a long, lossy operation.

## Do not

- **Do not raise the grace period** (`--time`, `terminationGracePeriodSeconds`) before measuring signal delivery and drain duration. A longer budget is valid when a proven graceful drain needs it; it only hides the problem when no useful drain is occurring.
- **Do not treat SIGKILL as the normal path.** A container that is always killed never gets to flush or drain, and that is a data-integrity problem, not a tidiness one.
- Do not add a `sleep` before exit to "let things finish"; drain on the signal instead.
- Do not diagnose exit `137` from the number alone. It records SIGKILL: a stop-timeout expiry, an OOM kill, or an explicit external kill can all produce it. Check engine state and the surrounding event.
