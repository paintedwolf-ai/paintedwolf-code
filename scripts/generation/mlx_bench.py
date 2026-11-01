"""Measure the pinned E4B reference with resident or disk-backed PLE."""

import argparse
import json
from pathlib import Path
import resource
import time

import mlx.core as mx
from mlx_lm import load
from mlx_lm.models.cache import make_prompt_cache
from mapped_ple import replace_ple


def measure(model, prompt, generated, chunk_size):
    cache = make_prompt_cache(model)
    mx.reset_peak_memory()
    usage_before = resource.getrusage(resource.RUSAGE_SELF)
    start = time.perf_counter()
    prefill_step_max = 0.0
    for offset in range(0, len(prompt) - 1, chunk_size):
        step = time.perf_counter()
        model(mx.array([prompt[offset:min(offset + chunk_size, len(prompt) - 1)]]), cache=cache)
        mx.eval([c.state for c in cache])
        prefill_step_max = max(prefill_step_max, time.perf_counter() - step)
    step = time.perf_counter()
    logits = model(mx.array([prompt[-1:]]), cache=cache)
    mx.eval(logits, [c.state for c in cache])
    prefill_step_max = max(prefill_step_max, time.perf_counter() - step)
    prefilling = time.perf_counter() - start
    token = mx.argmax(logits[:, -1, :], axis=-1)
    mx.eval(token)
    start = time.perf_counter()
    tokens = []
    decode_step_max = 0.0
    for _ in range(generated):
        step = time.perf_counter()
        tokens.append(token.item())
        logits = model(token[None], cache=cache)
        token = mx.argmax(logits[:, -1, :], axis=-1)
        mx.eval(token, [c.state for c in cache])
        decode_step_max = max(decode_step_max, time.perf_counter() - step)
    decoding = time.perf_counter() - start
    usage_after = resource.getrusage(resource.RUSAGE_SELF)
    return {"prefill_step_max_seconds": prefill_step_max, "decode_step_max_seconds": decode_step_max,
            "prefill_seconds": prefilling, "decode_seconds": decoding,
            "prompt_tps": len(prompt) / prefilling, "generation_tps": generated / decoding,
            "peak_mlx_bytes": mx.get_peak_memory(), "live_mlx_bytes": mx.get_active_memory(),
            "retained_mlx_bytes": mx.get_cache_memory(),
            "process_peak_rss_bytes": usage_after.ru_maxrss,
            "major_faults": usage_after.ru_majflt - usage_before.ru_majflt,
            "minor_faults": usage_after.ru_minflt - usage_before.ru_minflt,
            "block_inputs": usage_after.ru_inblock - usage_before.ru_inblock,
            "tokens": tokens}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", required=True)
    parser.add_argument("--input", type=Path, required=True)
    args = parser.parse_args()
    specification = json.loads(args.input.read_text())
    prompt = specification["prompt_tokens"]
    chunk_size = specification["prefill_chunk"]
    if not 1 <= chunk_size <= 256:
        raise ValueError("prefill chunk exceeds its bound")
    generated, trials, ple = (specification[k] for k in ("generation_tokens", "trials", "ple"))
    if not prompt or len(prompt) + generated > 65536 or generated < 1 or not 1 <= trials <= 20:
        raise ValueError("benchmark dimensions exceed their bounds")
    if any(type(t) is not int or not 0 <= t < 262144 for t in prompt):
        raise ValueError("invalid input token")
    mx.set_cache_limit(128 << 20)
    loading_start = time.perf_counter()
    model, tokenizer, config = load(args.model, lazy=True, return_config=True, trust_remote_code=False)
    text = config.get("text_config", config)
    if text.get("num_hidden_layers") != 42 or text.get("hidden_size") != 2560:
        raise ValueError("the reference benchmark only accepts E4B")
    if ple == "mapped":
        replace_ple(model, args.model, config)
    mx.eval(model.parameters())
    loading = {"load_seconds": time.perf_counter() - loading_start,
               "load_peak_mlx_bytes": mx.get_peak_memory(),
               "load_peak_process_rss_bytes": resource.getrusage(resource.RUSAGE_SELF).ru_maxrss}
    for trial in range(trials + 1):
        result = measure(model, prompt, generated, chunk_size)
        print(json.dumps({"trial": trial, "warmup": trial == 0, "ple": ple, **loading, **result}), flush=True)
        mx.clear_cache()


if __name__ == "__main__":
    main()
