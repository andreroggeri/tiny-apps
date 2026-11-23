"""HTTP API: POST a photo to /crop, get the straightened page back."""

import logging
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from tesourinha.crop import NoPage, crop

MAX_BODY = 50 << 20  # WhatsApp photos are a few MB

log = logging.getLogger("tesourinha")


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/healthz":
            self._reply(200, b"ok", "text/plain")
        else:
            self._reply(404, b"not found", "text/plain")

    def do_POST(self):
        if self.path != "/crop":
            self._reply(404, b"not found", "text/plain")
            return
        size = int(self.headers.get("Content-Length") or 0)
        if size <= 0 or size > MAX_BODY:
            self._reply(413 if size else 411, b"body missing or too big", "text/plain")
            return
        data = self.rfile.read(size)
        start = time.monotonic()
        try:
            out = crop(data)
        except NoPage as e:
            log.info("left as is: %s (%.2fs)", e, time.monotonic() - start)
            self._reply(204, b"", "text/plain")
            return
        except ValueError as e:
            self._reply(400, str(e).encode(), "text/plain")
            return
        log.info("cropped %d -> %d bytes (%.2fs)", size, len(out), time.monotonic() - start)
        self._reply(200, out, "image/jpeg")

    def _reply(self, status, body, ctype):
        self.send_response(status)
        if status != 204:
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if status != 204:
            self.wfile.write(body)

    def log_message(self, *args):
        pass  # one line per crop is logged above; skip the access log


def main():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    addr = os.environ.get("TESOURINHA_LISTEN", ":8081")
    host, _, port = addr.rpartition(":")
    server = ThreadingHTTPServer((host, int(port)), Handler)
    log.info("listening on %s", addr)
    server.serve_forever()


if __name__ == "__main__":
    main()
