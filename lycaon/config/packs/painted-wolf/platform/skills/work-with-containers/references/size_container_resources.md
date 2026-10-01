# Size container resources

Use this workflow to set or fix resource settings. On a process start that inspects or applies them, declare `docker`, `podman`, or `kubectl` in `capability_request.host_resources`.

The asymmetry that governs every decision here:

- **Memory limits are enforced reactively.** Under memory pressure, exceeding a cgroup limit makes processes in that cgroup candidates for an OOM kill. A kill is abrupt, but it need not happen at the instant one allocation crosses the number.
- **CPU is compressible.** Exceed the limit and the process is *throttled*, not killed. It keeps running, just slower — which surfaces as latency spikes and p99 blowup rather than as an error.

So the same word, "limit", means "hard ceiling enforced by death" for one and "rate cap enforced by waiting" for the other. Treating them alike is the root of most bad sizing.

## Requests and limits are different things

On Kubernetes:

- **Requests drive scheduling.** The scheduler reserves this much on a node and uses it to decide where the pod fits. Requests are what determine whether a pod is `Pending`.
- **Limits drive enforcement.** The cgroup ceiling at runtime. Limits have no effect on placement.

The two standard mistakes follow directly:

**Neither requests nor admission defaults set.** The scheduler accounts for little or no resource usage, packs the node full, and everything on it degrades together once real load arrives. A limit can also become the request when no request is declared, and a `LimitRange` may inject defaults, so inspect the effective Pod rather than inferring from the source manifest.

**Requests equal to limits everywhere, by reflex.** That can produce Guaranteed QoS, but it also reserves scheduling capacity at the peak value whether the workload normally uses it or not. Other workloads may use idle CPU or memory at runtime, but the scheduler cannot place new pods into capacity already accounted for by requests.

QoS class is only a useful approximation for node-pressure eviction order. The kubelet actually ranks pods by whether usage exceeds requests, then Pod Priority, then usage relative to requests. A Guaranteed pod can still be evicted under node pressure, and QoS does not determine scheduler preemption.

## Workflow

1. **Measure before setting anything.** `kubectl top pod` / `docker stats` for current usage, and the workload's peak under realistic load — not idle. Sizing from a guess produces either OOM kills or wasted capacity, and you cannot tell which until it hurts.
2. **Set memory requests from the working set and limits from observed peaks plus justified headroom.** Keeping them close can reduce eviction risk and scheduling surprises, but the correct gap depends on burst shape and node capacity.
3. **Set CPU requests from typical usage.** Be deliberate about CPU *limits*: a limit low enough to throttle a latency-sensitive service is a worse outcome than letting it burst, which is why many teams set CPU requests without CPU limits. Say which you chose and why.
4. **Confirm the runtime sees the cgroup limit.** Current runtimes are generally container-aware, but older versions, native allocations outside a managed heap, and explicit heap flags can still produce a process budget larger than the container's. Inspect the runtime's effective heap/container settings before adding overrides such as JVM heap flags or Node's `--max-old-space-size`.
5. **Re-measure after changing.** The evidence is the observed usage against the new numbers, not the absence of a crash so far.

## Reading the symptom

| Symptom | Cause |
|---|---|
| `OOMKilled`, exit `137` | Killed by an OOM path. Check the container cgroup limit and node-level OOM/pressure evidence before attributing it. |
| Slow under load, CPU flat at a ceiling | **CPU throttling** at the limit. Not an error anywhere; check throttling metrics. |
| `Pending`, "Insufficient cpu/memory" | **Requests** too large for any node. A scheduling problem, not a runtime one. |
| Evicted without exceeding its own limits | **Node pressure**. Inspect usage versus requests, Pod Priority, and relative overage; QoS alone does not identify the first candidate. |

## Do not

- **Do not raise a memory limit to stop repeated OOM kills without checking whether usage is stable.** Growth that never plateaus is a leak, and a bigger limit only buys time while making the eventual failure larger — see `diagnose-performance`.
- **Do not omit requests accidentally.** When a limit is set and no request exists for that resource, Kubernetes commonly defaults the request to the limit; that can reserve far more scheduling capacity than intended. Admission policy or a `LimitRange` may also supply defaults, so inspect the effective Pod spec.
- Do not copy resource numbers between environments or services. They are a property of the workload and its load.
- Do not treat an absence of OOM kills as proof the sizing is right; it may simply mean today's traffic was light.
