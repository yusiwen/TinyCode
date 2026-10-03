"""A minimal OpenAI-compatible endpoint, for scenarios that need a model.

It answers any chat completion with one fixed streaming answer, so a scenario can drive
the real request path — provider, SSE parsing, incremental render — with no network, no
key and no cost. Deliberately dumb: the request body is ignored, which is what makes it
deterministic. Started by the scenario that needs it; see prompt-answer.scenario.
"""
import http.server
import sys

ANSWER = "stub-answer-ok"       # keep in sync with prompt-answer.scenario
FINAL = "stub-final-ok"         # keep in sync with tool-call.scenario
# The first request in tool mode asks for one bash call; every later request answers with
# FINAL, so the agent loop terminates after one tool round instead of calling the tool
# forever (which is what a stub returning the same call every time does).
TOOL_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_stub","type":"function",'
    '"function":{"name":"bash","arguments":"{\\"command\\":\\"echo stub-tool-marker\\"}"}}]}}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}\n\n'
    'data: [DONE]\n\n'
)


TOOL_MODE = len(sys.argv) > 2 and sys.argv[2] == "tool"
_calls = {"n": 0}


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = b"".join(iter(lambda: self.rfile.read(1), b"")) if False else self.rfile.read(
            int(self.headers.get("content-length", 0))
        )
        del body  # ignored on purpose
        _calls["n"] += 1
        if TOOL_MODE and _calls["n"] == 1:
            payload = TOOL_CALL.encode()
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.send_header("content-length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return

        chunks = []
        for part in (FINAL if TOOL_MODE else ANSWER,):
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
