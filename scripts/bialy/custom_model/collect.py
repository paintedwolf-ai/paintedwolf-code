"""Export host-accepted candidates; quality attestations remain independent."""
import argparse
from contextlib import closing
from collections import Counter
import json
from pathlib import Path

from common import digest, file_digest, read_rows, write_new
from receipts import accepted_row, open_store


def collect(fleets, identity, catalog, out, quality=()):
    out = Path(out)
    out.mkdir(parents=True, exist_ok=False)
    quality_by_key = {}
    for item in quality:
        key = (item["request_sha256"], item["response_sha256"])
        if key in quality_by_key:
            raise ValueError("duplicate quality binding")
        quality_by_key[key] = item
    paths = sorted(p for root in fleets for p in Path(root).glob("shards/*/training-captures/*.json"))
    stores = {p.parent.parent / "store.db" for p in paths}
    inputs = set(paths) | stores | {p.with_name(p.name + "-wal") for p in stores if p.with_name(p.name + "-wal").exists()}
    before = {str(p): file_digest(p) for p in sorted(inputs)}
    counts = Counter()
    seen = set()
    with (out / "sft.receipts.jsonl").open("x") as stream:
        for path in paths:
            try:
                record = json.loads(path.read_text())
                with closing(open_store(path.parent.parent / "store.db")) as db:
                    db.execute("BEGIN")
                    row = accepted_row(record, db, identity, catalog)
                key = (row["session"], row["request_capture"]["request_id"])
                if key in seen:
                    raise ValueError("duplicate captured call")
                seen.add(key)
                report = row["validation"]
                quality_report = quality_by_key.get((report["request_sha256"], report["response_sha256"]))
                if quality_report:
                    report["quality"] = quality_report
                row["source"] = {"capture_sha256": before[str(path)], "store_sha256": before[str(path.parent.parent / "store.db")]}
                stream.write(json.dumps(row, ensure_ascii=False) + "\n")
                counts["accepted_candidates"] += 1
            except (ValueError, KeyError, TypeError) as exc:
                counts["excluded:" + str(exc)] += 1
    after_inputs = set(paths) | stores | {p.with_name(p.name + "-wal") for p in stores if p.with_name(p.name + "-wal").exists()}
    after = {str(p): file_digest(p) for p in sorted(after_inputs)}
    if before != after or paths != sorted(p for root in fleets for p in Path(root).glob("shards/*/training-captures/*.json")):
        raise ValueError("collection changed during export")
    write_new(out / "manifest.json", {"schema": "pw-receipt-export/1", "identity": identity, "inputs": before,
              "counts": dict(counts), "quality_sha256": digest(list(quality)),
              "rows_sha256": file_digest(out / "sft.receipts.jsonl")})
    return dict(counts)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fleet", action="append", required=True)
    parser.add_argument("--binding", type=Path, required=True)
    parser.add_argument("--catalog-sha256", required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--quality", type=Path)
    args = parser.parse_args()
    print(json.dumps(collect(args.fleet, json.loads(args.binding.read_text()), args.catalog_sha256, args.out,
                             read_rows(args.quality) if args.quality else [])))
