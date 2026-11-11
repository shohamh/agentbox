import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class H(BaseHTTPRequestHandler):
    def _answer(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n:
            self.rfile.read(n)
        host = (self.headers.get("Host") or "").split(":")[0]
        body = json.dumps({"service": host, "path": self.path, "method": self.command}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        print("corp-services: %s %s Host=%s from %s" % (self.command, self.path, host, self.client_address[0]), flush=True)

    do_GET = do_POST = do_PUT = do_HEAD = _answer

    def log_message(self, *a):
        pass


ThreadingHTTPServer(("0.0.0.0", 80), H).serve_forever()
