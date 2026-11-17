"""Greedy behaviour of an exported artifact against the checkpoint it was made from.

The reference is the merged checkpoint's own greedy continuation of each served
conversation fixture, with the logit margin of every step. An artifact must tokenize
each rendered prompt identically, and its greedy tokens may leave the reference only
at a step the reference itself decided by less than the artifact's tolerance.
"""
import json

from rendering import CASES, template_sha256

SCHEMA = "pw-behavior-reference/1"
NEW_TOKENS = 16
# Logit margin under which a different greedy token is attributed to the artifact's precision.
TOLERANCE = {"F16": 0.5, "Q4_K_M": 2.0, "Q5_K_M": 2.0, "Q8_0": 1.0, "affine-group64-f16": 2.0}
# Greatest logit difference between an adapter applied and merged. The two compute one
# function in a different order of fp32 operations, so they differ by rounding; a tenth of
# the narrowest artifact tolerance keeps the merge from moving any decision an artifact is held to.
MERGE_TOLERANCE = 0.05


def served_cases():
    """The fixtures a student server is sent: the wire form, ending where the model answers."""
    return [case for case in json.loads(CASES.read_text()) if case.get("wire")]


def rendered(tokenizer, cases=None):
    """Each served fixture as the prompt text and tokens training renders it to."""
    prompts = []
    for case in served_cases() if cases is None else cases:
        text = tokenizer.apply_chat_template(case["messages"], tools=case["tools"], tokenize=False,
                                             add_generation_prompt=True, enable_thinking=False)
        prompts.append({"name": case["name"], "prompt": text,
                        "prompt_tokens": tokenizer(text, add_special_tokens=False)["input_ids"]})
    return prompts


def reference(model, tokenizer, cases=None, new_tokens=NEW_TOKENS):
    import torch
    prompts = rendered(tokenizer, cases)
    for prompt in prompts:
        ids = prompt["prompt_tokens"]
        with torch.no_grad():
            out = model.generate(input_ids=torch.tensor([ids]), attention_mask=torch.ones(1, len(ids), dtype=torch.long),
                                 do_sample=False, max_new_tokens=new_tokens, output_logits=True,
                                 return_dict_in_generate=True, pad_token_id=tokenizer.eos_token_id or 0)
        prompt["tokens"] = out.sequences[0, len(ids):].tolist()
        prompt["margins"] = []
        for step in out.logits:
            top = torch.topk(step[0].float(), 2).values
            prompt["margins"].append(float(top[0] - top[1]))
    return {"schema": SCHEMA, "template_sha256": template_sha256(tokenizer.chat_template),
            "new_tokens": new_tokens, "prompts": prompts}


def merge_difference(before, after):
    """The greatest logit difference between the adapter applied and the merged
    checkpoint on one prompt. Raises beyond MERGE_TOLERANCE, and on a value that is not a number."""
    difference = float((before.float() - after.float()).abs().max())
    if not difference <= MERGE_TOLERANCE:
        raise ValueError("merged checkpoint departs from its adapter: greatest logit difference %r" % difference)
    return difference


def compare(expected, observed, precision):
    """What one artifact did with the reference prompts. Raises unless every prompt
    tokenized identically, every departure from the reference is inside the tolerance
    for `precision`, and most prompts agree on their first token."""
    tolerance = TOLERANCE[precision]
    agreement, departures = [], []
    for prompt, seen in zip(expected["prompts"], observed, strict=True):
        if seen["prompt_tokens"] != prompt["prompt_tokens"]:
            raise ValueError("artifact tokenizes a rendered prompt differently: " + prompt["name"])
        wanted, tokens = prompt["tokens"], seen["tokens"][:len(prompt["tokens"])]
        agreed = next((i for i, pair in enumerate(zip(wanted, tokens)) if pair[0] != pair[1]), min(len(wanted), len(tokens)))
        agreement.append(agreed)
        if agreed < len(wanted):
            departures.append({"prompt": prompt["name"], "step": agreed, "margin": prompt["margins"][agreed]})
    decisive = [item for item in departures if item["margin"] >= tolerance]
    if decisive:
        raise ValueError("artifact departs from the merged checkpoint beyond its tolerance: " + json.dumps(decisive))
    if 2 * sum(1 for agreed in agreement if agreed > 0) <= len(agreement):
        raise ValueError("artifact agrees with the merged checkpoint on too few first tokens")
    return {"precision": precision, "tolerance": tolerance, "agreed_tokens": agreement, "departures": departures}
