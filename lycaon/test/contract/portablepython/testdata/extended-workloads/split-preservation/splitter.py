import json
import os
import sys
import tempfile
from pathlib import Path


def split_dataset(input_path: Path, out_dir: Path) -> None:
    buckets: dict[str, list[str]] = {"train": [], "val": []}
    group_split: dict[str, str] = {}

    with input_path.open("r", encoding="utf-8") as fh:
        for lineno, raw in enumerate(fh, start=1):
            line = raw.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError as exc:
                raise ValueError(f"invalid JSON on line {lineno}: {exc}") from exc
            split = rec.get("split")
            group = rec.get("group")
            if split not in buckets or not isinstance(group, str) or not group:
                raise ValueError(f"invalid record on line {lineno}")
            prev = group_split.get(group)
            if prev is not None and prev != split:
                raise ValueError(f"cross-split group overlap for {group!r}: {prev} vs {split}")
            group_split[group] = split
            buckets[split].append(json.dumps(rec, sort_keys=True, separators=(",", ":")) + "\n")

    out_dir.mkdir(parents=True, exist_ok=True)
    staged: list[tuple[Path, Path]] = []
    try:
        for name in sorted(buckets):
            fd, tmp_name = tempfile.mkstemp(prefix=f".{name}.", suffix=".tmp", dir=out_dir)
            tmp_path = Path(tmp_name)
            with os.fdopen(fd, "w", encoding="utf-8") as tmp_fh:
                tmp_fh.writelines(buckets[name])
            staged.append((tmp_path, out_dir / f"{name}.jsonl"))
        for tmp_path, dest_path in staged:
            os.replace(tmp_path, dest_path)
    except Exception:
        for tmp_path, _ in staged:
            if tmp_path.exists():
                tmp_path.unlink()
        raise


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: splitter.py <input.jsonl> <out_dir>")
    try:
        split_dataset(Path(sys.argv[1]), Path(sys.argv[2]))
    except ValueError as err:
        sys.stderr.write(f"error: {err}\n")
        sys.exit(2)
