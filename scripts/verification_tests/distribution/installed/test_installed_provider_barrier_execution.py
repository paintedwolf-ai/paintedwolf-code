"""Only actual held fixture transport and peer-close facts witness response control."""
import json
import socket
import threading
import unittest
from urllib.request import Request, build_opener, ProxyHandler

from linux_release.installed_cases.provider import GUI_API_KEY, MODEL, ProviderFixture
from linux_release.installed_cases.provider_barrier import ResponseBarrier
from linux_release.installed_cases.records import CaseUnavailable


class BarrierTests(unittest.TestCase):
    def start(self, barrier):
        server, peer = socket.socketpair()
        self.addCleanup(server.close)
        self.addCleanup(peer.close)
        lease = barrier.capture(server, b'fixed request')
        results = []
        def wait():
            released = barrier.wait(lease)
            results.append(released)
            barrier.finish(lease, released)
        thread = threading.Thread(target=wait)
        thread.start()
        def stop():
            barrier.close()
            thread.join(timeout=2)
            self.assertFalse(thread.is_alive())
        self.addCleanup(stop)
        return server, peer, results, thread

    def test_independent_peer_close_is_distinct_from_fixture_cleanup(self):
        barrier = ResponseBarrier()
        _, peer, results, thread = self.start(barrier)
        held = barrier.require_held()
        peer.close()
        row = barrier.await_peer_cancellation()
        thread.join(timeout=2)
        self.assertEqual(row['request_id'], held['request_id'])
        self.assertEqual(row['cancellation'], 'peer-closed')
        self.assertTrue(row['connection_closed'])
        self.assertFalse(row['response_written'])
        self.assertEqual(results, [False])
        with self.assertRaises(CaseUnavailable):
            barrier.release()
        cleanup = ResponseBarrier()
        self.start(cleanup)
        cleanup.close()
        self.assertEqual(cleanup.snapshot()['cancellation'], 'fixture-cleanup')
        with self.assertRaises(CaseUnavailable):
            cleanup.await_peer_cancellation()

    def test_release_preserves_request_identity_and_wait_deadline_is_not_peer_cancellation(self):
        barrier = ResponseBarrier()
        server, _, results, thread = self.start(barrier)
        before = barrier.require_held()
        with self.assertRaises(ValueError):
            barrier.capture(server, b'another concurrent request')
        barrier.release()
        thread.join(timeout=2)
        row = barrier.snapshot()
        self.assertEqual(row['request_id'], before['request_id'])
        self.assertTrue(row['response_written'])
        self.assertEqual(results, [True])
        with self.assertRaises(CaseUnavailable):
            barrier.await_peer_cancellation()
        expired = ResponseBarrier(timeout_seconds=0.05)
        self.start(expired)
        self.assertTrue(expired.done.wait(2))
        self.assertEqual(expired.snapshot()['cancellation'], 'deadline-expired')
        with self.assertRaises(CaseUnavailable):
            expired.require_held()

    def test_real_authenticated_http_request_is_held_then_written_on_release(self):
        barrier = ResponseBarrier()
        calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
        with ProviderFixture.gui(calls, response_barrier=barrier) as fixture:
            request = {'model': MODEL, 'messages': [{'role': 'user', 'content': 'fixed fixture'}],
                'tools': [{'type': 'function', 'function': {'name': 'command'}}]}
            results, errors = [], []
            def send():
                try:
                    req = Request(fixture.base_url + '/chat/completions',
                        data=json.dumps(request).encode(),
                        headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + GUI_API_KEY})
                    with build_opener(ProxyHandler({})).open(req, timeout=5) as response:
                        results.append(json.loads(response.read()))
                except BaseException as error:
                    errors.append(error)
            thread = threading.Thread(target=send)
            thread.start()
            before = barrier.await_held()
            self.assertEqual(before['state'], 'held')
            self.assertEqual(results, [])
            barrier.release()
            thread.join(timeout=5)
            self.assertFalse(thread.is_alive())
            self.assertEqual(errors, [])
            self.assertEqual(results[0]['choices'][0]['finish_reason'], 'tool_calls')
            self.assertTrue(barrier.done.wait(2))
            self.assertTrue(barrier.snapshot()['response_written'])

    def test_invalid_deadlines_and_uncaptured_holds_refuse(self):
        for value in (0, -1, 181, True, float('nan')):
            with self.subTest(value=value), self.assertRaises(ValueError):
                ResponseBarrier(value)
        barrier = ResponseBarrier()
        with self.assertRaises(CaseUnavailable):
            barrier.require_held()
        barrier.close()
