"""Real HTTP range acceptance without public network or production credentials."""
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import re
import subprocess
import threading
import unittest
from unittest.mock import patch

import verify_client_public as public


class RangeHandler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        start, end = map(int, re.fullmatch(r'bytes=(\d+)-(\d+)', self.headers['Range']).groups())
        self.server.ranges.append((start, end))
        body = self.server.payload[start:end + 1]
        status = 206
        if self.server.mode == 'ignored':
            body, status = self.server.payload, 200
        elif self.server.mode == 'corrupt':
            body = bytes([body[0] ^ 1]) + body[1:]
        self.send_response(status)
        self.send_header('Content-Length', str(len(body)))
        reported = start + 1 if self.server.mode == 'wrong-range' else start
        self.send_header('Content-Range', f'bytes {reported}-{end}/{len(self.server.payload)}')
        self.end_headers()
        if self.server.mode == 'truncated':
            body = body[:len(body) // 2]
        self.wfile.write(body)


class PublicRangeTests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(('127.0.0.1', 0), RangeHandler)
        self.server.payload = bytes(range(97)) * 64
        self.server.mode = 'valid'
        self.server.ranges = []
        worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        worker.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.base = f'http://127.0.0.1:{self.server.server_port}'
        self.item = dict(filename='fixture.exe', downloadRoute='client',
                         size=len(self.server.payload), sha256=hashlib.sha256(self.server.payload).hexdigest())

    def verify_complete(self):
        with patch.object(public, 'PUBLIC_BASE', self.base), patch.object(public, 'RANGE_CHUNK', 1024):
            public.verify_complete_download(self.item)

    def test_all_bytes_are_covered_once_and_hashed_in_order(self):
        self.verify_complete()
        ranges = sorted(self.server.ranges)
        self.assertEqual(ranges[0][0], 0)
        self.assertEqual(ranges[-1][1], self.item['size'] - 1)
        self.assertTrue(all(previous[1] + 1 == current[0] for previous, current in zip(ranges, ranges[1:])))

    def test_wrong_content_range_is_rejected(self):
        self.server.mode = 'wrong-range'
        with self.assertRaisesRegex(ValueError, 'unacknowledged'):
            public.download_range(self.base, 0, 31, self.item['size'])

    def test_ignored_range_is_not_accepted_as_a_fragment(self):
        self.server.mode = 'ignored'
        with self.assertRaises((subprocess.CalledProcessError, ValueError)):
            public.download_range(self.base, 0, 31, self.item['size'])

    def test_truncated_range_is_rejected_after_bounded_retry(self):
        self.server.mode = 'truncated'
        with self.assertRaises(subprocess.CalledProcessError):
            public.download_range(self.base, 0, 31, self.item['size'])
        self.assertEqual(len(self.server.ranges), 2)

    def test_complete_digest_detects_corrupt_content(self):
        self.server.mode = 'corrupt'
        with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
            self.verify_complete()


if __name__ == '__main__':
    unittest.main()
