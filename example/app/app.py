import json
import os
import random
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

VERSION = os.environ.get("VERSION", "v1")
ITEMS = [{"id": i, "name": f"item-{i}", "price": round(10 + i * 1.5, 2)} for i in range(1, 51)]


class Handler(BaseHTTPRequestHandler):
    def send(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass

    def do_GET(self):
        time.sleep(random.uniform(0.02, 0.08) if VERSION == "v1" else random.uniform(0.005, 0.02))
        if self.path == "/healthz":
            return self.send(200, {"ok": True})
        if self.path == "/api/items":
            items = ITEMS if VERSION == "v1" else sorted(ITEMS, key=lambda x: -x["id"])
            return self.send(200, {"count": len(items), "results": items[:10]})
        if self.path.startswith("/api/items/"):
            try:
                item_id = int(self.path.rsplit("/", 1)[1])
            except ValueError:
                return self.send(400, {"detail": "bad id"})
            item = next((dict(i) for i in ITEMS if i["id"] == item_id), None)
            if item is None:
                return self.send(404, {"detail": "not found"})
            if VERSION == "v2" and item_id % 10 == 0:
                item["price"] = str(item["price"])
            item["updated_at"] = time.time()
            return self.send(200, item)
        return self.send(404, {"detail": "not found"})

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(length) or b"{}")
        if self.path == "/api/login":
            if not body.get("password"):
                return self.send(400 if VERSION == "v1" else 401, {"detail": "password required"})
            return self.send(200, {"user": body.get("username"), "token": f"tok-{random.random()}"})
        return self.send(404, {"detail": "not found"})


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8000), Handler).serve_forever()
