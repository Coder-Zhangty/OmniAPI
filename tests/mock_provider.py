"""Isolated protocol fixture. Never deploy as a real Provider."""
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATE = {"mode": "ok", "requests": [], "name": os.environ.get("MOCK_NAME", "mock")}
LOCK = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send_json(self, code, data):
        body = json.dumps(data).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/control":
            with LOCK:
                self.send_json(200, STATE)
        elif self.path == "/v1/models":
            self.send_json(200, {"object": "list", "data": [{"id": "upstream-a", "object": "model"},
                                                           {"id": "upstream-b", "object": "model"}]})
        else:
            self.send_json(404, {})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        if self.path == "/control":
            with LOCK:
                STATE["mode"] = body.get("mode", "ok")
                if body.get("reset"):
                    STATE["requests"] = []
            return self.send_json(200, {"ok": True})
        with LOCK:
            STATE["requests"].append({"model": body.get("model"), "path": self.path,
                                       "stream": body.get("stream", False)})
            mode = STATE["mode"]
        if mode == "disconnect":
            self.connection.close()
            return
        if mode in ("429", "500", "503"):
            return self.send_json(int(mode), {"error": {"message": "Synthetic provider failure",
                                                        "type": "server_error", "code": mode}})
        usage = {"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}
        if not body.get("stream"):
            return self.send_json(200, {"id": "chatcmpl-fixture", "object": "chat.completion", "created": int(time.time()),
                "model": body["model"], "choices": [{"index": 0, "message": {"role": "assistant", "content": "AI_GATEWAY_OK"},
                                                     "finish_reason": "stop"}], "usage": usage})
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        for word in ("AI_", "GATEWAY_", "OK"):
            chunk = {"id": "chatcmpl-fixture", "object": "chat.completion.chunk", "created": int(time.time()),
                     "model": body["model"], "choices": [{"index": 0, "delta": {"content": word}, "finish_reason": None}]}
            self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
            self.wfile.flush()
            time.sleep(0.35)
        chunk = {"id": "chatcmpl-fixture", "object": "chat.completion.chunk", "created": int(time.time()),
                 "model": body["model"], "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}], "usage": usage}
        self.wfile.write(("data: " + json.dumps(chunk) + "\n\ndata: [DONE]\n\n").encode())
        self.wfile.flush()


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8000), Handler).serve_forever()
