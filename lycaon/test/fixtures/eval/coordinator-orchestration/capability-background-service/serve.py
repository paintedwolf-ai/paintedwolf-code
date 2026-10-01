import json
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/receipt":
            self.send_error(404)
            return
        body = json.dumps({"receipt": Path("nonce.txt").read_text().strip()}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler, bind_and_activate=False)
server.server_bind()
Path("port.txt").write_text(str(server.server_port) + "\n")
time.sleep(2)
server.server_activate()
server.serve_forever()
