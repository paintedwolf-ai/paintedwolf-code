import time

def mark(label):
    print("PHASE", label, f"{time.perf_counter():.9f}", flush=True)

mark("start")
import yaml
mark("yaml_imported")
from openpyxl import Workbook, load_workbook
mark("openpyxl_imported")
from collections import Counter

with open("config/repos.yaml") as f:
    cfg = yaml.safe_load(f)
repos = cfg["repos"]

mark("yaml_loaded")
wb = Workbook()
ws = wb.active
ws.title = "repositories"
ws.append(["name", "language", "split", "license", "pinned_commit"])
for r in repos:
    ws.append([r["name"], r["language"], r["split"], r["spdx"], r["commit"]])
ws.freeze_panes = "A2"
for col, w in zip("ABCDE", [14, 12, 10, 20, 44]):
    ws.column_dimensions[col].width = w

counts = Counter((r["split"], r["language"]) for r in repos)
ws2 = wb.create_sheet("counts_by_split_language")
ws2.append(["split", "language", "count"])
for (split, lang), n in sorted(counts.items()):
    ws2.append([split, lang, n])
ws2.freeze_panes = "A2"
ws2.append([])
ws2.append(["split", "count"])
for split, n in sorted(Counter(r["split"] for r in repos).items()):
    ws2.append([split, n])

wb.save("repositories.xlsx")
mark("saved")

wb2 = load_workbook("repositories.xlsx")
assert wb2.sheetnames == ["repositories", "counts_by_split_language"]
r1 = wb2["repositories"]
assert r1.freeze_panes == "A2"
assert r1.max_row == len(repos) + 1
r2 = wb2["counts_by_split_language"]
assert r2.freeze_panes == "A2"
print("sheets:", wb2.sheetnames)
print("repos rows:", r1.max_row - 1, "freeze:", r1.freeze_panes, "|", r2.freeze_panes)
for row in r2.iter_rows(values_only=True):
    print(row)
mark("end")
