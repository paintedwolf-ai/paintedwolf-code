# Diagnose resource exhaustion

Use this workflow when something fails for no reason visible in the code. Exhaustion has one defining property: **the error surfaces wherever the next allocation happened to occur, which is almost never where the resource was consumed.** A build that fails on a small file copy, a network call that fails with a file error, a process killed with no stack trace — these are symptoms of the machine, not of the change you just made.

So the first move is never to fix. It is to measure which resource is gone.

## Workflow

1. **Identify the resource before touching anything.** Disk space, inodes, memory, and file descriptors each have a distinct check, and three of the four are invisible in the obvious one.
2. **Check whether the limit is the host's or a container's.** A process can be killed at a cgroup limit while the host has memory to spare. `free`/Activity Monitor describes the host and tells you nothing about a container's ceiling.
3. **Distinguish a leak from a limit that is genuinely too low.** Watch the number over time: monotonic growth under steady work is a leak, and raising the limit only delays it. A plateau just above the ceiling is a limit to raise.
4. **Find the consumer**, not just the total. A total tells you that you are out; only the consumer tells you what to do.
5. **Report the measurement with the fix.** "Raised the memory limit" is not a finding. "Observed steady-state 1.8 GB against a 1 GB limit" is.

## Where each resource hides

**Disk space.** `df -h` for space, and **`df -i` for inodes** — a filesystem can be completely out of inodes with space free, which produces "No space left on device" on a tiny write and is the most confusing member of this family. Millions of small files (caches, session files, node_modules) do this. Then `du -sh` down the tree to find the consumer.

Two disk traps worth knowing:

- **Deleted-but-open files.** A file unlinked while a process still holds it releases no space until that process exits. This is why `du` and `df` disagree, sometimes by a lot. `lsof +L1` lists them; the fix is restarting the holder, not deleting more.
- **Container build cache** is usually the largest single consumer on a development machine — often tens of gigabytes. `docker system df` breaks it down by images, containers, volumes, and cache.

**Memory.** For a killed process, correlate exit code `137` with `dmesg`/journal OOM evidence, the container's `.State.OOMKilled`, orchestrator events, and cgroup memory counters. No application error or stack trace is consistent with an abrupt kill but does not identify OOM by itself. Check the cgroup limit and node pressure before concluding the app leaks.

**File descriptors.** Errors read `EMFILE`, `ENFILE`, or "too many open files", and they surface as *whatever failed next* — a network dial, a file watcher that stops firing, a database connection refused. `ulimit -n` for the soft limit and the process's own `/proc/<pid>/fd` (or `lsof -p`) for what it holds. Unclosed files, sockets, and filesystem watchers are the usual leaks; a watcher leak in particular presents as "the tool stopped noticing changes" rather than as an error.

**Processes and threads.** Fork failures and "resource temporarily unavailable" under a thread or PID limit, common with runaway parallelism in a build.

## Do not

- **Do not run `docker system prune`, or any reclaim that deletes images, volumes, or caches, without asking.** Volumes hold the user's data and images may be unpushed. Report what `docker system df` shows and let them choose what goes.
- **Do not delete files to free space** without saying exactly what you are deleting. Logs, caches, and build output are usually safe; anything else is the user's call.
- **Do not raise a limit as the first move.** Without a measurement you cannot tell a leak from an under-provision, and raising the ceiling on a leak buys time while making the eventual failure larger.
- Do not conclude "out of memory" from a slow process — swapping and OOM are different, and so are their fixes.
- Do not assume the host's numbers describe a container's, or the reverse.
