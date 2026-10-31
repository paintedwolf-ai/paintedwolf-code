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


def measure(model, prompt, generated):
    cache = make_prompt_cache(model)
    mx.reset_peak_memory()
    usage_before = resource.getrusage(resource.RUSAGE_SELF)
    start = time.perf_counter()
    for offset in range(0, len(prompt) - 1, 256):
        model(mx.array([prompt[offset:min(offset + 256, len(prompt) - 1)]]), cache=cache)
        mx.eval([c.state for c in cache])
    logits = model(mx.array([prompt[-1:]]), cache=cache)
    mx.eval(logits, [c.state for c in cache])
    prefilling = time.perf_counter() - start
    token = mx.argmax(logits[:, -1, :], axis=-1)
    mx.eval(token)
    start = time.perf_counter()
    tokens = []
    for _ in range(generated):
        tokens.append(token.item())
        logits = model(token[None], cache=cache)
        token = mx.argmax(logits[:, -1, :], axis=-1)
        mx.eval(token, [c.state for c in cache])
    decoding = time.perf_counter() - start
    usage_after = resource.getrusage(resource.RUSAGE_SELF)
    return {"prefill_seconds": prefilling, "decode_seconds": decoding,
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
    generated, trials, ple = (specification[k] for k in ("generation_tokens", "trials", "ple"))
    if not prompt or len(prompt) + generated > 65536 or generated < 1 or not 1 <= trials <= 20:
        raise ValueError("benchmark dimensions exceed their bounds")
    if any(type(t) is not int or not 0 <= t < 262144 for t in prompt):
        raise ValueError("invalid input token")
    mx.set_cache_limit(128 << 20)
    model, tokenizer, config = load(args.model, lazy=True, return_config=True, trust_remote_code=False)
    text = config.get("text_config", config)
    if text.get("num_hidden_layers") != 42 or text.get("hidden_size") != 2560:
        raise ValueError("the reference benchmark only accepts E4B")
    if ple == "mapped":
        replace_ple(model, args.model, config)
    mx.eval(model.parameters())
    for trial in range(trials + 1):
        result = measure(model, prompt, generated)
        print(json.dumps({"trial": trial, "warmup": trial == 0, "ple": ple, **result}), flush=True)
        mx.clear_cache()


if __name__ == "__main__":
    main()
