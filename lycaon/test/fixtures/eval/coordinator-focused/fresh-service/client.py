import argparse
import json
from pathlib import Path
from urllib.request import ProxyHandler, build_opener
from reconcile import reconcile

parser = argparse.ArgumentParser()
parser.add_argument("--minimum", type=int, default=0)
args = parser.parse_args()
endpoint = json.loads(Path("endpoint.json").read_text())
with build_opener(ProxyHandler({})).open(endpoint["url"], timeout=5) as response:
    receipt = json.load(response)
events = json.loads(Path("events.json").read_text())
receipt["balances"] = reconcile(events, args.minimum)
Path("receipt.json").write_text(json.dumps(receipt) + "\n")
print(json.dumps(receipt))
