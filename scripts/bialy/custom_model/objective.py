"""Completion log likelihood and reference-relative sequence DPO."""
import torch
import torch.nn.functional as F


def scores(model, sample, device):
    ids = torch.tensor([sample["input_ids"]], device=device)
    labels = torch.tensor([sample["labels"]], device=device)[:, 1:]
    logits = model(input_ids=ids, attention_mask=torch.ones_like(ids)).logits[:, :-1].float()
    mask = labels.ne(-100)
    if not mask.any():
        raise ValueError("no completion labels")
    logp = F.log_softmax(logits, dim=-1).gather(-1, labels.clamp_min(0).unsqueeze(-1)).squeeze(-1)
    total = (logp * mask).sum()
    return total, -total / mask.sum()


def dpo(chosen, rejected, reference_chosen, reference_rejected, chosen_nll, beta, anchor):
    if beta <= 0 or anchor < 0:
        raise ValueError("beta must be positive and anchor nonnegative")
    margin = (chosen - reference_chosen.detach()) - (rejected - reference_rejected.detach())
    return -F.logsigmoid(beta * margin) + anchor * chosen_nll
