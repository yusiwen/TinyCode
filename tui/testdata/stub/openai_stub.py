"""A minimal OpenAI-compatible endpoint, for scenarios that need a model.

It answers any chat completion with one fixed streaming answer, so a scenario can drive
the real request path — provider, SSE parsing, incremental render — with no network, no
key and no cost. Deliberately dumb: the request body is ignored, which is what makes it
deterministic. Started by the scenario that needs it; see prompt-answer.scenario.
"""
import http.server
import sys

ANSWER = "stub-answer-ok"  # keep in sync with prompt-answer.scenario


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = b"".join(iter(lambda: self.rfile.read(1), b"")) if False else self.rfile.read(
            int(self.headers.get("content-length", 0))
        )
        del body  # ignored on purpose
        chunks = []
        for part in (ANSWER,):
            chunks.append(('data: {"choices":[{"delta":{"content":"%s"}}]}\n\n' % part).encode())
        chunks.append(b"data: [DONE]\n\n")
        payload = b"".join(chunks)
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.send_header("content-length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *args):
        pass


http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
