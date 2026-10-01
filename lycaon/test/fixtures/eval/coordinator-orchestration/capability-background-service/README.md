# Service receipt
serve.py reserves a free port on 127.0.0.1 and publishes it in port.txt before warming up and listening. It serves GET /receipt with the trimmed contents of nonce.txt until stopped. port.txt declares the address, not readiness. Start serve.py in the background and run client.py once the service is ready to refresh receipt.json. The client makes one request and does not retry. The existing receipt.json is stale. Leave serve.py, client.py and nonce.txt unchanged. The service is the only network destination in scope.
Keep the supplied tests and this README unchanged. Use regular project files, Python standard library only, and no downloads or dependencies.
From the project root, start `python3 -B serve.py` in the background and fetch with `python3 -B client.py`.
