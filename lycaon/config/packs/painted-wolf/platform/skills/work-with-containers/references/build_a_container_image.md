# Build a container image

Use this workflow to author or repair a Dockerfile. On a process start that builds, declare `docker` or `podman` in `capability_request.host_resources`.

Two facts drive nearly every decision here. **Each instruction makes a layer, and a layer is immutable** — deleting a file in a later layer hides it but does not reclaim its size. And **a changed layer invalidates every layer after it**, so instruction order decides whether a rebuild takes two seconds or ten minutes.

## Workflow

1. **Read the actual error, with full output.** `docker build --progress=plain` (BuildKit collapses logs by default and hides the failing command's output). The failing step and its output are almost always enough.
2. **Check the build context first when things are slow.** The client uploads the context to the daemon before any instruction runs. A missing `.dockerignore` sends `.git`, `node_modules`, and build artifacts every time. This is the most common cause of a build that is slow before it appears to do anything.
3. **Order instructions by rate of change** — the cache rule follows directly. Install system packages, then copy the dependency manifest and install dependencies, then copy the source. Copying source before installing dependencies re-runs the install on every source edit, which is the single most common cache mistake.
4. **Use a multi-stage build when the build needs tools the runtime does not.** Compile in a stage with the full toolchain, then `COPY --from=build` only the artifact into a slim runtime base. This is how you get a small image — not by deleting things later.
5. **Keep a package install and its cleanup in one `RUN`.** `apt-get update && apt-get install -y … && rm -rf /var/lib/apt/lists/*` as a single instruction. Split across two `RUN`s, the cleanup writes a new layer while the cache files stay in the earlier one — the image does not shrink, and a stale `update` layer can pair with a fresh `install` and fail on missing packages.
6. **Run as a non-root user** for anything long-lived: create the user, `chown` what it needs, then `USER`. Be aware that on Linux the container UID maps straight to the host for bind mounts, so a non-root container writing to a host-managed directory will hit permission errors that do not appear on macOS.
7. **Verify by building and running**, not by reading. Confirm the image starts and the artifact is where you expect. Report the image size before and after if size was the goal.

## Reading a build failure

| What you see | Where to look |
|---|---|
| Fails on `COPY` with "not found" | The path is relative to the **build context**, not the Dockerfile. It may also be excluded by `.dockerignore`. |
| Package install fails on a package that exists | A cached `apt-get update` layer paired with a fresh install. Combine them in one `RUN`. |
| Cache never hits | Something high in the file changes every build — often `COPY . .` placed before dependency install. |
| Works locally, fails in CI | Usually architecture (`--platform`), or a file present locally but gitignored and so absent from CI's context. |
| `exec format error` at run time | Architecture mismatch — an `amd64` image on `arm64` or the reverse. Build with `--platform` or use a multi-arch base. |

## Do not

- **Do not add `RUN rm -rf …` in a later layer to shrink an image.** The bytes live in the earlier layer; the image gets *bigger*. Remove in the same `RUN` that created them, or don't copy them in.
- **Do not use `latest` for a base image** in anything reproducible. Pin the tag, and pin the digest when the build must be repeatable.
- Do not put secrets in `ARG` or `ENV` — both are recoverable from image history. Use BuildKit's `--mount=type=secret`.
- Do not disable the cache (`--no-cache`) as a debugging reflex. It hides which layer actually changed and makes every iteration slow. Reach for it only to prove a caching hypothesis.
- Do not add a new base image or tool without checking what the project already uses; matching the existing base is usually correct.
