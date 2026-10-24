"""Resumable optimizer loop with bounded pilots and resource measurements."""
import random
import resource
import signal
import statistics
import sys
import time

from checkpoint import Checkpoints, atomic_json
from common import digest


def run_loop(model, optimizer, training, validation, loss, args, manifest, started):
    import torch
    from peft import get_peft_model_state_dict, set_peft_model_state_dict
    checkpoints = Checkpoints(args.out / "checkpoints", digest(manifest), args.keep_checkpoints)
    restored, rejected = checkpoints.restore() if args.resume else (None, [])
    cursor = {"epoch":0,"position":0,"step":0,"total":0.,"history":[],"validation_position":0,"validation_total":0.}
    if restored is not None:
        set_peft_model_state_dict(model, restored["adapter"])
        optimizer.load_state_dict(restored["optimizer"])
        torch.set_rng_state(restored["torch_rng"])
        if args.device.startswith("cuda"):
            torch.cuda.set_rng_state_all(restored["cuda_rng"])
        random.setstate(restored["python_rng"])
        cursor = restored["cursor"]
    stop = []
    previous = {}
    for sig in (signal.SIGINT, signal.SIGTERM):
        previous[sig] = signal.signal(sig, lambda signum, frame: stop.append(signum))
    invocation_steps, durations = 0, []

    def save():
        return checkpoints.save({"adapter":get_peft_model_state_dict(model),"optimizer":optimizer.state_dict(),
               "torch_rng":torch.get_rng_state(),"cuda_rng":torch.cuda.get_rng_state_all() if args.device.startswith("cuda") else [],
               "python_rng":random.getstate(),"cursor":cursor})

    def report(status):
        per_step = statistics.median(durations) if durations else None
        remaining = args.epochs * len(training) - cursor["step"]
        rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
        metrics = {"status":status,"step":cursor["step"],"total_steps":args.epochs*len(training),
                   "elapsed_seconds":time.monotonic()-started,"median_update_seconds":per_step,
                   "estimated_training_seconds_remaining":remaining*per_step if per_step is not None else None,
                   "observed_update_range_seconds":[min(durations),max(durations)] if durations else None,
                   "estimate_excludes":"model loading, reference scoring, validation, checkpoint I/O, export, and collection",
                   "peak_process_rss_bytes":rss if sys.platform == "darwin" else rss*1024,
                   "peak_cuda_bytes":torch.cuda.max_memory_allocated() if args.device.startswith("cuda") else None,
                   "rejected_checkpoints":rejected,"history":cursor["history"]}
        atomic_json(args.out / "progress.json", metrics)
        return metrics

    try:
        if restored is None:
            save()  # A first-update crash still has a known starting point.
        while cursor["epoch"] < args.epochs:
            model.train()
            order = list(range(len(training)))
            random.Random(args.seed + cursor["epoch"]).shuffle(order)
            while cursor["position"] < len(order):
                if stop or (args.max_seconds and time.monotonic()-started >= args.max_seconds) or (args.max_updates and invocation_steps >= args.max_updates):
                    save()
                    return report("paused")
                tick = time.monotonic()
                optimizer.zero_grad(set_to_none=True)
                value = loss(training[order[cursor["position"]]])
                if not torch.isfinite(value):
                    raise ValueError("nonfinite training loss")
                value.backward()
                torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0, error_if_nonfinite=True)
                optimizer.step()
                cursor["total"] += float(value.detach())
                cursor["position"] += 1
                cursor["step"] += 1
                invocation_steps += 1
                durations.append(time.monotonic()-tick)
                if cursor["step"] % args.checkpoint_every == 0:
                    save()
                    report("training")
            model.eval()
            with torch.no_grad():
                while cursor["validation_position"] < len(validation):
                    if stop or (args.max_seconds and time.monotonic()-started >= args.max_seconds):
                        save()
                        return report("paused_validation")
                    cursor["validation_total"] += float(loss(validation[cursor["validation_position"]]))
                    cursor["validation_position"] += 1
                    if cursor["validation_position"] % args.checkpoint_every == 0:
                        save()
            validation_loss = cursor["validation_total"] / len(validation)
            cursor["history"].append({"epoch":cursor["epoch"]+1,"train_loss":cursor["total"]/len(training),"validation_loss":validation_loss})
            cursor.update(epoch=cursor["epoch"]+1,position=0,total=0.,validation_position=0,validation_total=0.)
            save()
        return report("complete")
    finally:
        # An exception does not overwrite the last known-good optimizer state.
        for sig, handler in previous.items():
            signal.signal(sig, handler)
