import json
from pathlib import Path
from urllib.request import ProxyHandler, build_opener

endpoint = json.loads(Path("endpoint.json").read_text())
with build_opener(ProxyHandler({})).open(endpoint["url"], timeout=5) as response:
    receipt = json.load(response)
Path("receipt.json").write_text(json.dumps(receipt) + "\n")
print("Local connection verified")
