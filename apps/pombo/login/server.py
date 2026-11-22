#!/usr/bin/env python3
"""Superlogica login service for pombo. NOT IMPLEMENTED.

See README.md in this folder for the contract and how to validate it.
Pombo calls POST /login and expects 200 + a JSON cookie list (Playwright
context.cookies() shape), within 5 minutes.
"""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = 8000
_one_at_a_time = threading.Lock()


def login() -> list[dict]:
    """Log in to the condo site and return the browser's cookies.

    The emailed code can be read from the otp-relay Worker (README.md,
    "Getting the emailed code").

    TODO: implement. Return context.cookies() after the browser reaches
    https://kpgadministrado.superlogica.net/clients/areadocondomino/...
    Raise an exception with a short reason on failure.
    """
    raise NotImplementedError("superlogica login is not implemented yet")


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if not _one_at_a_time.acquire(timeout=300):
            return self._send(409, "text/plain", b"another login is running")
        try:
            cookies = login()
        except Exception as e:  # pombo logs the reason
            return self._send(500, "text/plain", str(e).encode())
        finally:
            _one_at_a_time.release()
        self._send(200, "application/json", json.dumps(cookies).encode())

    def _send(self, status, ctype, body):
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    ThreadingHTTPServer(("", PORT), Handler).serve_forever()
