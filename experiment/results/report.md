# Numbered source experiment

**Stopped early by the lead. The decision rule was not applied, and this report gives no restore or stay-stripped verdict.** PRs #1878 to #1891 make `command` the sole entry point and remove callable `read` and `summarize`, so the projection under test is going away for live models.

Brief: `briefs/numbered-experiment.md`. Branch `exp/numbered-source`, throwaway, no PR. Binary built from source at fb6a8dfdf0. Both arms include the label fix 1c5a745ead, so the block label ends on the last line shown.

## Crux

- Showing numbers made citations much more accurate. GLM landed on a wrong line 0 times in 160 with numbers, against 70 of 160 without.
- Label copying was rare, and it appeared only on a file built to provoke it. Every full copy was rejected by the edit tool.
- One file was silently damaged by label confusion, in GLM numbered `colon_digits-14`.
- Only `summarize` was tested. `read` is on no live roster. The result does not directly test `cat -n` or `grep -n` under the `command` design.

## What ran

| Model | Rounds | Runs | Setup |
|---|---|---|---|
| GLM Flash (`glm-5p3-flash`) | 16 complete | 224 | Default investigate surface, each run to idle or a 420 s cap |
| E4B (`gemma4:e4b-paintedwolf`) | 3 complete, round 4 partial | 51 of a planned 112; the in-flight run was discarded | `edit` and `replace_lines` on the floor in both arms; edit runs stop once the file is exactly right; 900 s cap |

E2B did not run.

Arms differ only in the projection switch and the edit clause, confirmed by diffing the config trees and reading requests. Runs were interleaved over the same fixture and task order. There is no seed control.

## Results

Success means final bytes are correct, no stray paths, and the run settled.

| Model | Class | Success, unlabeled | Success, numbered | Final bytes, unlabeled | Final bytes, numbered |
|---|---|---|---|---|---|
| GLM | whitespace | 6/16 | 2/16 | 6/16 | 4/16 |
| GLM | tabs | 15/16 | 16/16 | 15/16 | 16/16 |
| GLM | multiline | 15/16 | 16/16 | 15/16 | 16/16 |
| GLM | colon_digits | 12/16 | 15/16 | 12/16 | 15/16 |
| GLM | duplicate | 16/16 | 16/16 | 16/16 | 16/16 |
| E4B | whitespace | 1/4 | 0/4 | 1/4 | 0/4 |
| E4B | tabs | 4/4 | 4/4 | 4/4 | 4/4 |
| E4B | multiline | 4/4 | 3/4 | 4/4 | 3/4 |
| E4B | colon_digits | 0/4 | 0/4 | 0/4 | 0/4 |
| E4B | duplicate | 1/3 | 1/4 | 1/3 | 1/4 |

| Edit measure | GLM unlabeled | GLM numbered | E4B unlabeled | E4B numbered |
|---|---|---|---|---|
| Edit calls | 228 | 251 | 57 | 64 |
| Failed edit calls | 108 | 121 | 27 | 38 |
| Calls hitting `EDITOR_DOCUMENT_READ_REQUIRED` | 106 | 116 | 17 | 15 |
| Calls hitting `EDIT_OLD_STRING_NOT_FOUND` | 2 | 5 | 5 | 14 |
| Full line label in `old_string` | 0 | 2 | 0 | 2 |
| Full line label in `new_string` | 0 | 0 | 0 | 1 |
| Partly stripped label | 0 | 3 | 0 | 5 |
| Line number label in a final file | 0 | 0 | 0 | 0 |
| Label in commit messages or notes | 0 | 0 | 0 | 0 |
| Mean input tokens, edit run | 116,169 | 131,154 | 90,717 | 108,564 |

Label counts were read by hand from the flagged calls. The script's `true_label_new` undercounts, because it misses the E4B `new_string` copy. Its `label_copies` also flags one GLM junk `old_string` in numbered and two in unlabeled that carry no line label.

Full copies and partial strips all occurred on `colon_digits`, the file whose lines look like labels. The other classes had none in 215 GLM and 52 E4B numbered edit calls.

## Citation accuracy by what the model was shown

All 76 citation runs made exactly one `summarize` call and no `command` call, so each answer rests on one delivery. The window was `invoice.go` lines 9–96 or `parser.py` lines 10–75. Both arms also received metadata anchors, for example `Total` at line 84.

| Model | Task | Shown | Exact | Paired line | Other wrong | No parseable citation |
|---|---|---|---|---|---|---|
| GLM | invoice | Label and anchors only | 23 | 3 | 54 | 0 |
| GLM | invoice | Numbered rows | 79 | 1 | 0 | 0 |
| GLM | parser | Label and anchors only | 34 | 30 | 16 | 0 |
| GLM | parser | Numbered rows | 47 | 33 | 0 | 0 |
| E4B | invoice | Label and anchors only | 3 | 0 | 7 | 5 |
| E4B | invoice | Numbered rows | 9 | 1 | 0 | 5 |
| E4B | parser | Label and anchors only | 0 | 0 | 10 | 5 |
| E4B | parser | Numbered rows | 5 | 0 | 0 | 10 |

"Paired line" is the guard or action line next to the scored line, for example the `if` above a `raise`. In the unlabeled arm a count can land there by accident, so that column overstates it.

GLM runs with all five items exact or paired: 16 of 16 and 16 of 16 numbered, against 5 of 16 and 10 of 16 unlabeled.

Unlabeled errors look like counting. In GLM invoice runs, offsets are constant within a run, such as +2 on all five items in 5 runs. E4B unlabeled parser citations are off by 26 to 31 lines.

In the E4B numbered arm, 3 of 6 runs ended in a host-assembled answer, against 1 of 6 unlabeled. That is why it has more unparseable citations. One of those answers gave bare or label-shaped numbers such as `86: amount -= …`, which the scorer records as missing.

## The numbered off-by-ones

The requests explain all 34 GLM cases. None is a misread number.

| Item | Cited line | Text at that line | Scored line | Count |
|---|---|---|---|---|
| parser a | 52 | `if len(raw) > MAX_LINE_BYTES:` | 53, the `raise` | 14 |
| parser e | 61 | `if not KEY_PATTERN.match(key):` | 62, the `raise` | 9 |
| parser d | 58 | `if not sep:` | 59, the `raise` | 6 |
| parser b | 56 | `return None` | 55, the `if` | 4 |
| invoice e | 92 | `if cents < 0 {` | 93, the `return` | 1 |

The delivered rows carry the right numbers, for example `52: if len(raw)…` and `53: raise ParseError…`. Each citation is the correct number for a line that does part of the item.

The items said "where X is refused", and I fixed the action line as truth before any run. That wording allows the guard, so these 34 reflect a strict answer key, not a coordinate error. This is my reading of the requests; a model's intent cannot be proved from them. The same rows also show no drift, unlike the unlabeled arm.

## Why edits failed, from the requests

- **Partial `summarize` windows, both arms.** This is most of the failure count. The window starts at the first symbol, so it is partial and establishes no edit base. The first edit is rejected, the model runs `sed` or `cat`, and it retries. That comparison is therefore dominated by a host rule the arms share.
- **GLM whitespace.** My prompt was ambiguous: "region column value" fits both the dict entry and `f"{region:>8}"`.
  - Numbered: 7 runs changed the f-string, 5 realigned the dict entry, 4 were right.
  - Unlabeled: 5, 5 and 6.
  - No labels appear in these diffs. The 2 numbered runs with correct bytes that scored as failures left a stray `report/__pycache__/`.
- **E4B whitespace.** It wrote a literal backslash-n in `old_string` (`'Totals:  \\n'`) in both arms, then also targeted the f-string.
- **`colon_digits`, numbered.** The prefix on a lookalike line reads `     4:      3: nightly-backup`.
  - GLM copied it whole twice and stripped it badly three times. All five were rejected.
  - E4B `colon_digits-2` copied the label into both arguments. It was rejected because `old_string` did not match.
  - **Silent damage:** GLM `colon_digits-14` went to `replace_lines` after its rejected edits and wrote `     12: cache-warm-full`, one space too many. The 12 is the YAML key, not a line number, so this is not a label in the file. It is still wrong bytes that came after label confusion, and the scorer's "label in final" flag on this run is a false positive.
  - E4B `colon_digits-4` stripped one space too many, and the substring matched, leaving a stray space on the emptied line.
- **`colon_digits`, unlabeled.** GLM failed differently: 2 runs stopped after `summarize` with no edit, and 2 used a multi-line `old_string` that silently deleted rows in between. No label was available to copy.
- **E4B `colon_digits`, both arms.** All 8 runs missed. 7 left an emptied line, usually because `old_string` omitted the newline. Numbered `colon_digits-1` made no edit.
- **E4B `duplicate`, both arms.** It rewrote more than asked. No labels were involved.
- **Not read:** two E4B numbered `NOT_FOUND` calls on `retryMax  = 3` in `tabs-2`. That run still finished correct.

## What the data can and cannot support

**It supports:**
- On `summarize` windows, per-line numbers remove counting errors on GLM. E4B points the same way, on few runs.
- With numbers shown, GLM did not produce a wrong coordinate in 160 items.
- On ordinary source, label copying into `edit` was not seen in 267 numbered calls.
- Lookalike lines are the real risk. That file drew all the copies and the one damaged file.

**It does not support:**
- Any rule outcome. If `rule.py` is run it prints "KEEP STRIPPED" for both models, but that is not a verdict. For GLM, the two failing clauses are the per-class reading I set for whitespace (2 vs 6, or exactly at the bound on bytes, 4 vs 6) and the false-positive label flag above. E4B has too few runs for the one-in-eight bound.
- Conclusions about `read`, which no live model can reach, or about `command` output. Under #1878 to #1891 the closest analogue is `cat -n` or `grep -n`, whose format and JSON escaping differ. Numbers there are an inference from this data, not a measurement.
- A small-model success rate. E4B has 3 or 4 runs per class, a changed floor, and early stopping. E2B was not run.
- Whether the arms differ on general edit success. The prompt steered models to `summarize`, the failures are dominated by a shared rule, and one fixture task was ambiguous.
- Generalising past this fixture. It is small and has only 7 tasks, one model per class, and no seed control. Citation truth is one fixed line per item.

## Harness incidents, all before any result was read

- The first GLM batch ran without the clause because `LYCAON_CONFIG_ROOT` needs a `config/` child. 74 runs and 4 pilots moved unread to `artifacts/numbered/invalid-no-clause/`, excluded. I found this by reading requests. The harness then refused any run missing its arm's clause.
- A DNS failure for the provider voided 4 GLM runs (`artifacts/numbered/void/`). The void rule depends only on "provider unreachable" and those slots were redone. That is the only repeat of any slot.
- An incomplete E4B numbered config failed to boot twice and recorded nothing. I rebuilt it. Those boot failures made rounds 1 and 2 report done early, and the restart reset the 6 hour budget clock.

## Cost

GLM list-price estimate: **$2.10** ($1.56 measured, $0.53 for the excluded runs and pilots, $0 for voided runs). This is computed from usage at the prices in `tools/mc14/analyze.py`, not from an invoice. E4B ran locally. The cost column in `table.md` for E4B is a hypothetical at GLM prices.

## Files

- Harness and scorer: `worktrees/numbered/experiment/`
- Table and scored runs: `artifacts/numbered/table.md`, `artifacts/numbered/scored.json`, copied to the branch under `experiment/results/`
- Raw requests: `artifacts/numbered/runs/`
