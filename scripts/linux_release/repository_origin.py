"""Serve an owned repository origin with a disposable explicitly trusted TLS certificate."""

from contextlib import contextmanager
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import ssl
import subprocess
import threading


@contextmanager
def serve(work: Path):
    directory = work / "origin"
    directory.mkdir()
    certificate, private = work / "fixture-tls.crt", work / "fixture-tls.key"
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                    "-subj", "/CN=Painted Wolf repository fixture", "-addext",
                    "subjectAltName=IP:127.0.0.1", "-keyout", str(private), "-out", str(certificate)],
                   stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, check=True)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(certificate, private)
    server = ThreadingHTTPServer(("127.0.0.1", 0), partial(SimpleHTTPRequestHandler, directory=str(directory)))
    server.socket = context.wrap_socket(server.socket, server_side=True)
    thread = threading.Thread(target=server.serve_forever)
    thread.start()
    try:
        yield "https://127.0.0.1:" + str(server.server_port), certificate
    finally:
        server.shutdown()
        server.server_close()
        thread.join()
