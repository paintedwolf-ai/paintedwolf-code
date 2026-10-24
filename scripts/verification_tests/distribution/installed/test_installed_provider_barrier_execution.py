"""Only actual held fixture transport and peer-close facts witness response control."""
import json
import socket
import threading
import unittest
from urllib.request import Request, build_opener, ProxyHandler
from urllib.error import HTTPError

from linux_release.installed_cases.provider import GUI_API_KEY, MODEL, ProviderFixture
from linux_release.installed_cases.provider_barrier import ResponseBarrier
from linux_release.installed_cases.records import CaseUnavailable


class BarrierTests(unittest.TestCase):
    def start(self, barrier, *, write_success=True):
        server, peer = socket.socketpair()
        self.addCleanup(server.close)
        self.addCleanup(peer.close)
        lease = barrier.capture(server, b'fixed request')
        results = []
        def wait():
            released = barrier.wait(lease)
            results.append(released)
            barrier.finish(lease, released and write_success)
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
        self.assertEqual(barrier.await_response_written()['request_id'], before['request_id'])
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
        self.http_barrier()
        calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
        with ProviderFixture.gui(calls) as previous:
            endpoint = previous.base_url
        # The same canonical endpoint and the response barrier must coexist;
        # recovery cannot silently choose another port or bypass real transport.
        self.http_barrier(endpoint)

    def test_acquisition_and_recovered_endpoint_share_the_real_response_barrier(self):
        calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
        with ProviderFixture.gui(calls) as previous:
            endpoint = previous.base_url
        self.http_barrier(endpoint, acquire='command')
        self.http_barrier(acquire='command', stream=True)

    def http_barrier(self, endpoint=None, *, acquire=None, stream=False):
        barrier = ResponseBarrier()
        calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
        with ProviderFixture.gui(calls, endpoint=endpoint, acquire=acquire,
                response_barrier=barrier) as fixture:
            if endpoint is not None:
                self.assertEqual(fixture.base_url, endpoint)
            request = {'model': MODEL, 'messages': [{'role': 'user', 'content': 'fixed fixture'}],
                'tools': [{'type': 'function', 'function': {'name': 'request_tools' if acquire else 'command'}}],
                'stream': stream}
            if acquire:
                invalid = Request(fixture.base_url + '/chat/completions',
                    data=json.dumps({**request, 'tools': []}).encode(),
                    headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + GUI_API_KEY})
                with self.assertRaises(HTTPError) as refused:
                    build_opener(ProxyHandler({})).open(invalid, timeout=5)
                self.assertEqual(refused.exception.code, 400)
                self.assertEqual(fixture.transport_observations[-1]['refusal_code'], 'acquisition-surface-invalid')
                self.assertFalse(fixture.acquisition.issued)
                self.assertIsNone(barrier.record)
            results, errors = [], []
            def send():
                try:
                    req = Request(fixture.base_url + '/chat/completions',
                        data=json.dumps(request).encode(),
                        headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + GUI_API_KEY})
                    with build_opener(ProxyHandler({})).open(req, timeout=5) as response:
                        wire = response.read().decode()
                        if stream:
                            chunks = wire.split('\n\n')
                            self.assertEqual(chunks[1], 'data: [DONE]')
                            row = json.loads(chunks[0].removeprefix('data: '))
                            row['choices'][0]['message'] = row['choices'][0]['delta']
                        else:
                            row = json.loads(wire)
                        results.append(row)
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
            call, = results[0]['choices'][0]['message']['tool_calls']
            self.assertEqual(call['function']['name'], 'request_tools' if acquire else 'command')
            if acquire:
                self.assertTrue(fixture.observations[0]['acquisition_requested'])
                self.assertFalse(fixture.observations[0]['completed'])
            self.assertTrue(barrier.await_response_written()['response_written'])

    def test_extra_connection_input_and_failed_response_never_certify_release(self):
        barrier = ResponseBarrier()
        _, peer, _, thread = self.start(barrier)
        peer.sendall(b'extra request bytes')
        self.assertTrue(barrier.done.wait(2))
        thread.join(timeout=2)
        self.assertEqual(barrier.snapshot()['cancellation'], 'unexpected-connection-input')
        with self.assertRaises(CaseUnavailable):
            barrier.await_response_written()
        failed = ResponseBarrier()
        _, _, _, thread = self.start(failed, write_success=False)
        failed.release()
        thread.join(timeout=2)
        self.assertEqual(failed.snapshot()['cancellation'], 'response-write-failed')
        with self.assertRaises(CaseUnavailable):
            failed.await_response_written()

    def test_invalid_deadlines_and_uncaptured_holds_refuse(self):
        for value in (0, -1, 181, True, float('nan')):
            with self.subTest(value=value), self.assertRaises(ValueError):
                ResponseBarrier(value)
        barrier = ResponseBarrier()
        with self.assertRaises(CaseUnavailable):
            barrier.require_held()
        barrier.close()
