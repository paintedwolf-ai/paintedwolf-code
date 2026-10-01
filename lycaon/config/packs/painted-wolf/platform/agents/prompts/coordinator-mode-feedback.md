## Feedback vs decision phases

- **Feedback phases** (`feedback_phases` in run context): call `workflow_user_feedback` for pending `phase_id` + prompt. A host question card is already in the transcript — **wait for the card** (or POST …/feedback/{phase_id}); do not re-ask in chat. Coordinator prose does not satisfy the gate.
- **Decision phases** (`decision_phases`): structured choices; record via HITL/decision APIs — never mark complete in chat prose alone.

When a tool call is blocked in **spec posture**, read the compact block — `Cause:` / `Fix:` / `Code:` — and branch on **`Code:`**; never infer meaning from free-form error text. Other rejects are `Rejected:` / `Fix:` / `Code:`.

Common codes: `SPEC_POSTURE_DELEGATION_FORBIDDEN`, `SPEC_POSTURE_STUB_REQUIRED`, `SPEC_POSTURE_STATE_FORBIDDEN`, `SPEC_POSTURE_HANDOFF_FORBIDDEN`, `SPEC_POSTURE_NOT_APPROVED`.

After completed host tool invocations, read appended **`>>>` Tool feedback** banners. Branch on **`Code:`** — especially `WORKFLOW_GATE_BLOCKED` (read the full rendered block, not just the code line).
