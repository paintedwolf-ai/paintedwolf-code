# Debug a Kubernetes workload

Use this workflow when a pod is unhealthy. On a process start that talks to the cluster, declare `kubectl` in `capability_request.host_resources`.

The loop is always the same — **status, then events, then logs** — and the useful part is that the status tells you *which of the three holds the answer*. A pod that never started has nothing in its logs, and reading logs first is the most common way to waste a diagnosis.

```
kubectl get pod <p>                     # which failure state
kubectl describe pod <p>                # Events (at the bottom) + Last State
kubectl logs <p> [--previous] [-c <c>]  # only once a container has actually run
```

## Which state means what

| Status | What it is | Where the answer is |
|---|---|---|
| **Pending** | Accepted by the cluster, but one or more containers are not ready to run. This includes scheduling and image/setup time. | **Conditions and events.** If `PodScheduled=False`, inspect requests, selectors/affinity, taints, and PVCs. If scheduled, inspect container waiting reasons such as image pulls or config setup. |
| **ImagePullBackOff** / **ErrImagePull** | The kubelet cannot pull the image. | **Events**, which carry the registry's actual error. Usually missing `imagePullSecrets`, a wrong tag, a rate limit, or an image built for another architecture. |
| **CrashLoopBackOff** | The container starts, exits, and Kubernetes backs off before retrying — up to five minutes. | **`Last State` in describe** for the exit code, then **`logs --previous`**. Kubernetes is working correctly here; your container is exiting. |
| **OOMKilled** | A process in the container was killed by an OOM path. A cgroup limit is common, but node-level OOM can also select a container. | `Last State` shows `OOMKilled`, usually exit `137`; compare limits and usage, then inspect node memory pressure/OOM evidence. See `work-with-containers`. |
| **Running, not Ready** | The container is up; the readiness probe is failing. | **Events** and the probe definition. Often the probe is wrong — wrong path or port, or `initialDelaySeconds` shorter than real startup — and the app is fine. |
| **Evicted** | The node ran out of disk or memory and reclaimed the pod. | Node conditions. Not the pod's fault; look at the node and at QoS class. |
| **Init:Error** / **Init:CrashLoopBackOff** | An *init* container failed, so the main container never ran. | `logs -c <init-container>`. Its name is in describe. |

## Details that catch people

- **`logs` shows the current container, which for a crash loop has just started and has nothing.** `--previous` is what you want — the output of the incarnation that actually died.
- **Events have cluster-configured finite retention** and are best-effort, supplemental evidence. An empty event list on an old problem can mean the events aged out, not that nothing happened.
- **Multi-container pods need `-c`** for both `logs` and `exec`; without it you get the first container, which may not be the failing one.
- **`exec` needs a running container.** For a `CrashLoopBackOff` there is nothing to exec into — debug from the image instead, or use a debug/ephemeral container.
- **`CrashLoopBackOff` is not itself a diagnosis.** Once you have the exit code and the previous logs, it is an ordinary failing container: `work-with-containers` has the exit-code taxonomy.

## Do not

- **Do not delete the pod to "restart" it.** It destroys the events and previous logs that hold the diagnosis, and a Deployment or StatefulSet recreates an identical pod that fails identically. You have lost the evidence and changed nothing.
- **Do not scale to zero and back, or roll the deployment, before capturing the state.**
- **Do not edit live resources with `kubectl edit`** to test a fix. The change is invisible to the repository, is reverted by the next apply, and cannot be reviewed. Change the manifest.
- Do not raise a memory limit or a probe timeout to make the symptom stop without evidence of the real usage or the real startup time.
- Do not act on a cluster without confirming the context first: `kubectl config current-context`. The wrong context is how a diagnosis becomes an incident.
