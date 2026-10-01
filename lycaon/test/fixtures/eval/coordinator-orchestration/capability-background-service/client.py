import json
from pathlib import Path
from urllib.request import ProxyHandler, build_opener

port = int(Path("port.txt").read_text().strip())
with build_opener(ProxyHandler({})).open(f"http://127.0.0.1:{port}/receipt", timeout=5) as response:
    receipt = json.load(response)
Path("receipt.json").write_text(json.dumps(receipt) + "\n")
print(json.dumps(receipt))
