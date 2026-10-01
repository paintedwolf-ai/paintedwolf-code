import argparse,json,urllib.request
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument("--minimum",type=int,default=0);p.add_argument("--offline",action="store_true");args=p.parse_args()
if args.offline: receipt=json.loads(Path("fallback.json").read_text())["receipt"]
else:
    with urllib.request.urlopen(json.loads(Path("endpoint.json").read_text())["url"]) as response: receipt=json.load(response)["receipt"]
balances={}
for event in json.loads(Path("events.json").read_text()): balances[event["account"]]=balances.get(event["account"],0)+event["cents"]
Path("receipt.json").write_text(json.dumps({"receipt":receipt,"balances":[{"account":a,"cents":v} for a,v in sorted(balances.items()) if v>=args.minimum]}))
