---
name: challenge-a-security-claim
description: Test whether a vulnerability or hardening claim is exploitable under the stated threat model.
---

# Challenge a security claim

1. Name the stated threat model from the assignment. If none was given, treat exploitability as Unknown and say what model would settle it.
2. For each vulnerability claim, name the **adversary** and **precondition** it needs.
3. If that pair is outside the stated model, refute it as a vulnerability. It may still be honest hardening.
4. Existence of a line, a missing header, or a checklist miss is not exploitability. Read the path and say whether an in-scope adversary can actually use it.
5. Ground every challenge in `path:line`. A challenge you cannot tie to observed code is an Unknown.
6. Verdict per claim: `survives` (exploitable under the model) or `refuted` (not exploitable under the model, or ungrounded). Do not assign severity.
