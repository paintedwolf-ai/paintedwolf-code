import argparse
import json
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("--out", required=True)
args = parser.parse_args()
report = {"exported": True, "items": 3}
Path(args.out).write_text(json.dumps(report) + "\n")
print(json.dumps(report))
