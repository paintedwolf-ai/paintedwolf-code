"""Export host-accepted candidates from episode bundles; quality attestations remain independent."""
import argparse
from collections import Counter
import json
from pathlib import Path

from bundle import Bundle
from common import digest, file_digest, read_rows, write_new
from contracts import check
from receipts import accepted_row


def collect(bundles, identity, catalog, out, quality=()):
    out = Path(out)
    out.mkdir(parents=True, exist_ok=False)
    quality_by_key = {}
    for item in quality:
        key = (item["request_sha256"], item["response_sha256"])
        if key in quality_by_key:
            raise ValueError("duplicate quality binding")
        quality_by_key[key] = item
    opened = [Bundle(root) for root in sorted(map(Path, bundles))]
    before = {str(item.root): item.manifest_sha256 for item in opened}
    counts = Counter(bundles=len(opened))
    seen = set()
    selected = tuple(identity.get("student_" + key) for key in ("provider", "kind", "model"))
    peers = {tuple(student[key] for key in ("provider", "kind", "model"))
             for student in identity.get("students", {}).values()} - {selected}
    with (out / "sft.receipts.jsonl").open("x") as stream:
        for item in opened:
            for record in item.captures:
                student = tuple(record.get("student_" + key) for key in ("provider", "kind", "model"))
                if student in peers:
                    counts["other_student"] += 1
                    continue
                try:
                    row = accepted_row(record, item, identity, catalog)
                    key = (row["session"], row["request_capture"]["request_id"])
                    if key in seen:
                        raise ValueError("duplicate captured call")
                    seen.add(key)
                    report = row["validation"]
                    quality_report = quality_by_key.get((report["request_sha256"], report["response_sha256"]))
                    if quality_report:
                        report["quality"] = quality_report
                    row["source"] = {"bundle_sha256": item.manifest_sha256, "capture_sha256": digest(record)}
                    check("sft-row", row)
                    stream.write(json.dumps(row, ensure_ascii=False) + "\n")
                    counts["accepted_candidates"] += 1
                    counts["accepted_by:" + report["acceptance"]] += 1
                except (ValueError, KeyError, TypeError) as exc:
                    counts["excluded:" + str(exc)] += 1
    if before != {str(item.root): Bundle(item.root).manifest_sha256 for item in opened}:
        raise ValueError("a bundle changed during export")
    write_new(out / "manifest.json", {"schema": "pw-receipt-export/2", "identity": identity, "bundles": before,
              "counts": dict(counts), "quality_sha256": digest(list(quality)),
              "rows_sha256": file_digest(out / "sft.receipts.jsonl")})
    return dict(counts)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", action="append", required=True, help="an episode bundle directory; repeat for each")
    parser.add_argument("--binding", type=Path, required=True)
    parser.add_argument("--catalog-sha256", required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--quality", type=Path)
    args = parser.parse_args()
    print(json.dumps(collect(args.bundle, json.loads(args.binding.read_text()), args.catalog_sha256, args.out,
                             read_rows(args.quality) if args.quality else [])))
