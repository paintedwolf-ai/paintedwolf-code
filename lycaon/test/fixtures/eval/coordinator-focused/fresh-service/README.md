# Current service receipt

The working client reads endpoint.json and events.json, obtains the service receipt, and writes receipt.json with the receipt and reconciled balances. Refresh it from the current service with a minimum balance of 100. The function default remains 0. The existing receipt is stale. Preserve client.py, reconcile.py, events.json, endpoint.json, and the supplied tests. The endpoint is the only network destination in scope.

Run the client with python3 -B client.py --minimum 100. Local unit checks use python3 -B -m unittest discover.

Keep the supplied tests and README unchanged. Use regular project files and the Python standard library; no dependencies or downloads.
