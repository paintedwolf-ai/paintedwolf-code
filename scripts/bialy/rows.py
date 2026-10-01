"""Training rows: `lycaon-debug decide export` output, pw-decide-row/1.

row.schema.json beside this file describes the shape. A row is one turn
decision: the state the engine read, the candidates the turn offered, what
the engine answered, and what the session then did. Every consumer reads
option sets from the row's `offered` candidates and option texts from the
corpus, so a label is always read against the options the host had.
"""

import json

SCHEMA = "pw-decide-row/1"


def load(path):
    """Every row of a JSON-lines file, refusing any row of another shape."""
    rows = []
    with open(path, encoding="utf-8") as fh:
        for n, line in enumerate(fh, 1):
            if not line.strip():
                continue
            row = json.loads(line)
            if row.get("schema") != SCHEMA:
                raise ValueError("%s:%d: schema %r, want %r" % (path, n, row.get("schema"), SCHEMA))
            rows.append(row)
    return rows


def engine_answered(row):
    """The engine chose what this turn carried, so a tool it preloaded and the session
    then called is not an independent label."""
    return row["engine"]["state"] == "answered"


def paired(first, second):
    """{card: (low, high)} of two judges' 0..4 levels. Without a second judge a card pairs
    with itself; with one, a card it left unscored is left out, since one score is not
    agreement."""
    if second is None:
        return {k: (v, v) for k, v in (first or {}).items()}
    return {k: (min(v, second[k]), max(v, second[k])) for k, v in (first or {}).items() if k in second}


def second(row, unit):
    """The second judge's scores for a unit: None when no second judge saw the row, and
    empty when it saw the row but could not score that unit."""
    scores = row["labels"].get("second_scores")
    if scores is None:
        return None
    return scores.get(unit) or ({} if unit != "requests" else [])


def skill_pairs(row):
    return paired(row["labels"].get("skill_scores"), second(row, "skills"))


def tool_pairs(row):
    return paired(row["labels"].get("tool_scores"), second(row, "tools"))


def need_pairs(row, n):
    needs = second(row, "requests")
    theirs = None if needs is None else ((needs[n] if n < len(needs) else None) or {})
    return paired(row["labels"]["requests"][n].get("scores"), theirs)


def relevant(pairs):
    """Cards both judges scored likely or certain."""
    return {k for k, (low, _) in pairs.items() if low >= 3}


def level(pair):
    """One training level from two judges: their mean, halves rounded down."""
    return (pair[0] + pair[1]) // 2


# How a turn's tool labels are read (--tool-truth). `consensus` marks a tool needed when
# both judges scored it likely or certain, unneeded when both scored it at most
# unlikely, and leaves the rest unlabelled; `judged` takes the first judge's likely or
# certain tools; `called` takes every tool the turn called. Tools the model asked for
# by name are needed under every rule. The judged rules fall back to `called` on a row
# without tool scores.
TOOL_TRUTH = ("consensus", "judged", "called")


def tool_targets(row, rule="consensus"):
    """{tool: 1, 0, or None} for every loadable tool the row offered; None is unlabelled.
    Under the judged rules a tool a judge left unscored stays unlabelled."""
    labels = row["labels"]
    offered = row["offered"]["loadable"]
    if rule == "called" or not labels.get("tool_scores"):
        targets = {t: 0 for t in offered}
        targets.update({t: 1 for t in labels["tools"]})
    elif rule == "judged":
        first = labels["tool_scores"]
        targets = {t: (1 if first[t] >= 3 else 0) if t in first else None for t in offered}
    else:
        pairs = tool_pairs(row)
        targets = {t: (1 if pairs[t][0] >= 3 else (0 if pairs[t][1] <= 1 else None)) if t in pairs else None for t in offered}
    targets.update({t: 1 for t in labels["requested_names"]})
    return targets


def truth_tools(row, rule="consensus"):
    """The loadable tools the turn needed, under a TOOL_TRUTH rule."""
    return {t for t, v in tool_targets(row, rule).items() if v == 1}
