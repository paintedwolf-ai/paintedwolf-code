"""Completion log likelihood and reference-relative sequence DPO."""
import torch
import torch.nn.functional as F


def completion_positions(labels):
    """Positions whose next token is a completion label; only their logits matter."""
    keep = (labels[0, 1:] != -100).nonzero().squeeze(-1)
    if keep.numel() == 0:
        raise ValueError("no completion labels")
    return keep


def scores(model, sample, device):
    ids = torch.tensor([sample["input_ids"]], device=device)
    labels = torch.tensor([sample["labels"]], device=device)
    keep = completion_positions(labels)
    # A full-vocabulary log-softmax over every prompt position does not fit a
    # consumer GPU; the model computes logits only where a completion token is predicted.
    logits = model(input_ids=ids, attention_mask=torch.ones_like(ids), logits_to_keep=keep).logits
    if logits.shape[:2] != (1, keep.numel()):
        raise ValueError("model ignored logits_to_keep")
    logp = F.log_softmax(logits[0].float(), dim=-1).gather(-1, labels[0, 1:][keep].unsqueeze(-1)).squeeze(-1)
    total = logp.sum()
    return total, -total / keep.numel()


def dpo(chosen, rejected, reference_chosen, reference_rejected, chosen_nll, beta, anchor):
    if beta <= 0 or anchor < 0:
        raise ValueError("beta must be positive and anchor nonnegative")
    margin = (chosen - reference_chosen.detach()) - (rejected - reference_rejected.detach())
    return -F.logsigmoid(beta * margin) + anchor * chosen_nll
