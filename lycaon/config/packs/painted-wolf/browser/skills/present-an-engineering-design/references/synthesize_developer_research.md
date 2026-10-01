# Synthesize developer research

Enter only when primary research material exists: interviews, observations, support feedback, usability sessions, task traces, or an explicit supplied dataset. If none exists, produce a research plan or discussion guide; never fabricate participants, quotes, session counts, or findings.

1. Build an **evidence inventory** with `source id`, `participant or cohort`, `method`, `date`, `scope`, `artifact`, and `limitations`. Replace identifying participant details with stable labels unless the user explicitly requires attribution and has authority to use it.
2. Extract **atomic observations**. Each row contains one behavior or statement, source id, context, direct quote or paraphrase, and analyst note. Cap a first pass at 100 observations; if more exist, sample transparently by method or cohort and report what remains.
3. Code observations into a **theme matrix**. A theme needs support from at least two independent sources unless labeled an anecdote. Record `theme`, `supporting sources`, `counterevidence`, `strength`, `affected workflow`, and `design implication`.
4. Identify at least one **productive contradiction** when the evidence contains one: a pair of needs or behaviors that appear opposed but constrain a better design. Do not manufacture tension merely because the layout includes a contradiction card.
5. Write a **claim-first synthesis** in this order: core finding, study scope, thesis, one representative quote, theme matrix, contradiction, workflow implications, research gaps, and limits. Distinguish observations from interpretation and recommendations visually and verbally.
6. Read [the developer-research layout](references/synthesize_developer_research-layout.md), then render an editorial report. Use `render_view` for a bounded synthesis board; use a local print route when the evidence ledger, coded observations, or appendix spans multiple pages.
7. Check coverage arithmetic: every displayed count can be traced to the inventory, every quote has a source id, every strong theme has supporting and contrary evidence, and every recommendation names the evidence that motivated it. On missing provenance, downgrade or remove the claim.
8. Inspect the artifact for readable quotes, non-truncated theme rows, visible limits, and accessible contrast. Do not make participant counts look like statistical prevalence when the method is qualitative.
9. Return a **research handoff** with `core_finding`, `scope`, `themes`, `contradictions`, `implications`, `open_questions`, `limits`, `evidence_inventory`, and the artifact or local route.

Worked theme row:

| Theme | Support | Counterevidence | Strength | Implication |
|---|---|---|---|---|
| Verification receipts build trust | 14 of 18 sessions across three roles | Two participants preferred raw terminal output | Strong qualitative signal | Lead review with command, scope, freshness, and result; retain raw output behind disclosure |

If privacy, consent, or provenance is unclear, exclude verbatim quotes and use aggregated paraphrases. State the excluded material and why; do not treat access to a transcript as permission to publish a person's words.
