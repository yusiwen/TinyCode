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
# "multi" asks for two bash calls in one step; "fail" asks for one that fails. Both then
# answer with FINAL, like every other tool mode.
MULTI_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function",'
    '"function":{"name":"bash","arguments":"{\\"command\\":\\"echo a\\"}"}},'
    '{"index":1,"id":"call_b","type":"function",'
    '"function":{"name":"bash","arguments":"{\\"command\\":\\"echo b\\"}"}}]}}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}\n\n'
    'data: [DONE]\n\n'
)
FAIL_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_f","type":"function",'
    '"function":{"name":"bash","arguments":"{\\"command\\":\\"exit 3\\"}"}}]}}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}\n\n'
    'data: [DONE]\n\n'
)
# "todo" asks the agent to write one task-list entry — the store is in-memory, so this is
# the only way a scenario can make the panel appear (see the todo scenario).
TODO_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_t","type":"function",'
    '"function":{"name":"todo","arguments":"{\\"todos\\":[{\\"content\\":\\"stub-todo-item\\",\\"status\\":\\"pending\\"}]}"}}]}}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}\n\n'
    'data: [DONE]\n\n'
)
WRITE_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function",'
    '"function":{"name":"write_file","arguments":"{\\"path\\":\\"/tmp/stub-outside-write.txt\\",\\"content\\":\\"stub\\"}"}}]}}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}\n\n'
    'data: [DONE]\n\n'
)
TOOL_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_stub","type":"function",'
    '"function":{"name":"bash","arguments":"{\\"command\\":\\"echo stub-tool-marker\\"}"}}]}}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}\n\n'
    'data: [DONE]\n\n'
)


# "tool" asks for a bash call, "write" for a write_file outside the workspace (which is
# what makes the TUI ask for approval in build mode — see permission-allow.scenario).
MODE = sys.argv[2] if len(sys.argv) > 2 else ""
_calls = {"n": 0}


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = b"".join(iter(lambda: self.rfile.read(1), b"")) if False else self.rfile.read(
            int(self.headers.get("content-length", 0))
        )
        del body  # ignored on purpose
        _calls["n"] += 1
        if MODE and _calls["n"] == 1:
            payload = {
                "tool": TOOL_CALL,
                "write": WRITE_CALL,
                "multi": MULTI_CALL,
                "fail": FAIL_CALL,
                "todo": TODO_CALL,
            }[MODE].encode()
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.send_header("content-length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return

        chunks = []
        for part in (FINAL if MODE else ANSWER,):
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
