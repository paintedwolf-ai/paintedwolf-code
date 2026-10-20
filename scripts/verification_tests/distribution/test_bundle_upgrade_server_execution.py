"""Local TLS serves only authenticated fixture routes and records each request."""
import http.client
import json
import os
from pathlib import Path
import socket
import ssl
import tempfile
import unittest
from unittest.mock import patch

from release_upgrade.host import HOST, certificates, local_origin
from release_upgrade.server import serving


@unittest.skipUnless(os.name == "posix", "The release TLS fixture runs on POSIX hosts")
class ServerTests(unittest.TestCase):
    def test_tls_validates_the_origin_and_routes_are_exact(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            ca, certificate, key = certificates(root)
            payload = root / "latest.json"
            payload.write_bytes(b'{"version":"1.1.0"}')
            log = root / "requests.jsonl"
            with serving({"/updates/preview/key-1/latest.json": payload}, certificate, key, log, port=0) as server:
                context = ssl.create_default_context(cafile=str(ca))
                connection = socket.create_connection(server.server_address, timeout=5)
                with context.wrap_socket(connection, server_hostname=HOST) as stream:
                    stream.sendall(b"GET /updates/preview/key-1/latest.json HTTP/1.0\r\nHost: downloads.paintedwolf.dev\r\n\r\n")
                    response = http.client.HTTPResponse(stream)
                    response.begin()
                    self.assertEqual(response.status, 200)
                    self.assertEqual(response.read(), payload.read_bytes())
                    self.assertEqual(response.headers["Cache-Control"], "no-store")
                for route in ("/", "/../latest.json", "/updates/preview/key-1/latest.json?other=true"):
                    connection = socket.create_connection(server.server_address, timeout=5)
                    with context.wrap_socket(connection, server_hostname=HOST) as stream:
                        stream.sendall(f"GET {route} HTTP/1.0\r\nHost: {HOST}\r\n\r\n".encode())
                        response = http.client.HTTPResponse(stream)
                        response.begin()
                        self.assertEqual(response.status, 404)
                        response.read()
                connection = socket.create_connection(server.server_address, timeout=5)
                with connection, self.assertRaises(ssl.SSLCertVerificationError):
                    context.wrap_socket(connection, server_hostname="wrong.example")
            requests = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertEqual([row["status"] for row in requests], [200, 404, 404, 404])

    def test_failed_startup_removes_only_the_owned_origin_mapping_and_certificate(self):
        import release_upgrade.host as host
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            ca = root / "ca.pem"
            ca.write_text("public test CA")
            process = unittest.mock.Mock()
            process.poll.return_value = 1
            result = unittest.mock.Mock(stdout=b"public DER", returncode=1)
            with patch.object(host, "certificates", return_value=(ca, root / "server.pem", root / "server.key")), \
                 patch.object(host, "run", return_value=result) as run, \
                 patch.object(host, "change_hosts") as hosts, \
                 patch.object(host.subprocess, "Popen", return_value=process), \
                 patch.object(host.socket, "getaddrinfo", return_value=[(None, None, None, None, ("127.0.0.1", 443))]):
                with self.assertRaisesRegex(ValueError, "did not become ready"):
                    with local_origin(root, root, root):
                        self.fail("server startup should fail")
            self.assertEqual(hosts.call_count, 2)
            self.assertEqual(hosts.call_args_list[0].args[0], hosts.call_args_list[1].args[0])
            self.assertEqual([call.args[1] for call in hosts.call_args_list], [False, True])
            self.assertTrue(any("delete-certificate" in call.args[0] for call in run.call_args_list))
            self.assertTrue(json.loads((root / "host-cleanup.json").read_text())["success"])
