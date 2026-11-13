"""Export training candidates from episode bundles; admission happens in the factory."""
import argparse
from collections import Counter
import json
from pathlib import Path

from bundle import Bundle
from common import digest, file_digest, write_new
from contracts import check
from receipts import candidate


def collect(bundles, identity, catalog, out):
    out = Path(out)
    out.mkdir(parents=True, exist_ok=False)
    opened = [Bundle(root) for root in sorted(map(Path, bundles))]
    before = {str(item.root): item.manifest_sha256 for item in opened}
    counts = Counter(bundles=len(opened))
    seen = set()
    selected = tuple(identity.get("student_" + key) for key in ("provider", "kind", "model"))
    peers = {tuple(student[key] for key in ("provider", "kind", "model"))
             for student in identity.get("students", {}).values()} - {selected}
    with (out / "candidates.jsonl").open("x") as stream:
        for item in opened:
            for record in item.captures:
                student = tuple(record.get("student_" + key) for key in ("provider", "kind", "model"))
                if student in peers:
                    counts["other_student"] += 1
                    continue
                try:
                    row = candidate(record, item, identity, catalog)
                    key = (row["session"], row["call_id"])
                    if key in seen:
                        raise ValueError("duplicate captured call")
                    seen.add(key)
                    row["source"] = {"bundle_sha256": item.manifest_sha256, "capture_sha256": digest(record)}
                    check("training-candidate", row)
                    stream.write(json.dumps(row, ensure_ascii=False) + "\n")
                    counts["host_excluded" if row.get("host_exclusion") else "candidates"] += 1
                except (ValueError, KeyError, TypeError) as exc:
                    counts["unbuildable:" + str(exc)] += 1
    if before != {str(item.root): Bundle(item.root).manifest_sha256 for item in opened}:
        raise ValueError("a bundle changed during export")
    write_new(out / "manifest.json", {"schema": "pw-candidate-export/1", "identity": identity, "bundles": before,
              "counts": dict(counts), "candidates_sha256": file_digest(out / "candidates.jsonl")})
    return dict(counts)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", action="append", required=True, help="an episode bundle directory; repeat for each")
    parser.add_argument("--binding", type=Path, required=True)
    parser.add_argument("--catalog-sha256", required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(collect(args.bundle, json.loads(args.binding.read_text()), args.catalog_sha256, args.out)))
