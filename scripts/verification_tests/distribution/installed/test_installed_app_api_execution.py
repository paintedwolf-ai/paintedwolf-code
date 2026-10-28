"""The installed engine API client speaks only to its captured loopback endpoint."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading
import unittest

from linux_release.installed_cases.app_api import EngineAPI
from linux_release.installed_cases.records import CaseUnavailable

LARGE = (8 << 20) + 2


class Handler(BaseHTTPRequestHandler):
    seen: list[dict] = []

    def log_message(self, *_):
        return

    def reply(self, status: int, body: bytes):
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        Handler.seen.append({'method': 'GET', 'path': self.path, 'authorization': self.headers.get('Authorization')})
        if self.path == '/health':
            self.reply(200, b'{"ok": true}')
        elif self.path == '/v1/redirect':
            self.send_response(302)
            self.send_header('Location', '/health')
            self.send_header('Content-Length', '0')
            self.end_headers()
        elif self.path == '/v1/large':
            self.reply(200, b'"' + b'a' * (LARGE - 2) + b'"')
        else:
            self.reply(404, b'{"error": "no such route"}')

    def do_POST(self):
        length = int(self.headers.get('Content-Length', '0'))
        payload = json.loads(self.rfile.read(length))
        Handler.seen.append({'method': 'POST', 'path': self.path, 'authorization': self.headers.get('Authorization'),
                             'payload': payload})
        self.reply(201, json.dumps({'echo': payload}).encode())


class EngineAPITests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.port = cls.server.server_address[1]

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join(timeout=5)

    def setUp(self):
        Handler.seen.clear()

    def test_endpoint_requires_a_loopback_port_and_a_whole_token(self):
        for port, token in [(0, None), (65536, None), ('8787', None), (8787, ''), (8787, 'a b'), (8787, 'tok\n')]:
            with self.subTest(port=port, token=token), self.assertRaises(ValueError):
                EngineAPI(port, token)
        self.assertEqual(EngineAPI(8787, 'tok')._origin, 'http://127.0.0.1:8787')

    def test_requests_stay_on_fixed_local_product_routes(self):
        api = EngineAPI(self.port, 'tok')
        for method, path, payload in [('HEAD', '/health', None), ('GET', 'http://127.0.0.1/health', None),
                                      ('GET', '//evil/v1/x', None), ('GET', '/v1/x#frag', None),
                                      ('GET', '/other', None), ('GET', '/healthz', None)]:
            with self.subTest(method=method, path=path), self.assertRaises(ValueError):
                api.request(method, path, payload)
        with self.assertRaises(ValueError):
            api.request('POST', '/v1/echo', {'blob': 'x' * (1 << 20)})
        self.assertEqual(Handler.seen, [])

    def test_public_health_is_the_only_request_without_a_credential(self):
        public = EngineAPI(self.port)
        self.assertEqual(public.request('GET', '/health'), {'status': 200, 'body': {'ok': True}})
        self.assertIsNone(Handler.seen[-1]['authorization'])
        for method, path, payload in [('POST', '/health', None), ('GET', '/v1/sessions', None), ('GET', '/health', {})]:
            with self.subTest(method=method, path=path), self.assertRaises(CaseUnavailable):
                public.request(method, path, payload)
        self.assertEqual(len(Handler.seen), 1)

    def test_credentialed_requests_carry_the_bearer_token_and_return_error_bodies(self):
        api = EngineAPI(self.port, 'tok')
        self.assertEqual(api.request('POST', '/v1/echo', {'n': 1}), {'status': 201, 'body': {'echo': {'n': 1}}})
        self.assertEqual(Handler.seen[-1], {'method': 'POST', 'path': '/v1/echo', 'authorization': 'Bearer tok',
                                            'payload': {'n': 1}})
        self.assertEqual(api.request('GET', '/v1/missing'), {'status': 404, 'body': {'error': 'no such route'}})

    def test_redirects_and_oversized_bodies_are_refused(self):
        api = EngineAPI(self.port, 'tok')
        with self.assertRaises(ValueError):
            api.request('GET', '/v1/redirect')
        self.assertEqual([row['path'] for row in Handler.seen], ['/v1/redirect'])
        with self.assertRaises(ValueError):
            api.request('GET', '/v1/large')


if __name__ == '__main__':
    unittest.main()
