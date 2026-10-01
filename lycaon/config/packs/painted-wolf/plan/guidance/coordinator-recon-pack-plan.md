<!-- recon-pack-plan-orient:v1 -->
Frame the user's explore ask and stamp the first cheap scout wave. This is not the final research plan: workers discover the paths that make a precise drill possible.

1. Use the user's ask plus existing board / transcript context. Do not make repository calls when that is enough to ask useful scout questions. If no useful scope can be stated at all, take **at most one** bounded orientation call, then stop planning.
2. Choose **1–{{ max_fanout_legs }}** read-only scout legs with allowlisted `agent_type` values. Broad recon normally starts with `repo-researcher` for project/build shape and `path-explorer` for entrypoints/runtime paths; targeted recon may need only one or two legs.
3. Call `fanout_plan` with the legs (each with a plain `subject`), then `workflow_advance` when `fanout_planned` is satisfied.

Give each leg a distinct question, expected evidence, and a finish condition. Functional territory is enough when filenames are not known yet. Deliberate overlap is valid when it independently confirms an important boundary.

Do not front-load deep inspection, scan work, or a complete package inventory. The reconcile phase decides whether the scout evidence answers the ask or justifies one targeted drill wave.
