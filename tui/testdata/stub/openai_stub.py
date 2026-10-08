"""A minimal OpenAI-compatible endpoint, for scenarios that need a model.

It answers any chat completion with one fixed streaming answer, so a scenario can drive
the real request path — provider, SSE parsing, incremental render — with no network, no
key and no cost. Deliberately dumb: the request body is ignored, which is what makes it
deterministic. Started by the scenario that needs it; see prompt-answer.scenario.
"""
import http.server
import json
import os
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
# "read" asks for read_file on a file in the working directory. It exists because reading a
# file is what starts the language server: the warmup in tool/filesystem.go is the reachable
# path after issue #111, and the LSP scenario needs the server up before /diagnostics can say
# anything other than "not available".
READ_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_r","type":"function",'
    '"function":{"name":"read_file","arguments":%s}}]}}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}\n\n'
    'data: [DONE]\n\n'
) % json.dumps(json.dumps({"path": os.path.join(os.getcwd(), "main.go")}))
# "lsp" asks for one lsp_symbols call on a file in the working directory — the LSP tool
# route, with no file read in front of it. The name must be the one lsp.ToolFactory
# registers (ToolDocumentSymbols = "lsp_symbols"); a stub that invents a name gets
# "unknown tool: ..." back from the agent loop, which is indistinguishable in a log that
# only records the result size.
LSP_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_l","type":"function",'
    '"function":{"name":"lsp_symbols","arguments":%s}}]}}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}\n\n'
    'data: [DONE]\n\n'
) % json.dumps(json.dumps({"file_path": os.path.join(os.getcwd(), "main.go")}))
# "confine" asks for one bash call that writes two files: one inside the working directory
# (a writable root) and one at a fixed path outside it. The scenario's own shell checks both
# paths after the program exits, so the assertion is about the filesystem rather than about
# the command's output — and it is selective on purpose: a host that refuses the command
# wholesale leaves *neither* file, which the "inside" half catches instead of reporting a
# working boundary. The outside path is deliberately NOT under the temp directory the file
# fence allows for other purposes; the command roots are the project plus its auto-allowed
# paths only.
CONFINE_INSIDE = ".stub-confine-inside.txt"
CONFINE_OUTSIDE = "/tmp/tinycode-stub-confine-outside.txt"
CONFINE_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_c","type":"function",'
    '"function":{"name":"bash","arguments":%s}}]}}]}\n\n'
    'data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}\n\n'
    'data: [DONE]\n\n'
) % json.dumps(json.dumps({
    "command": "echo inside > %s; echo outside > %s" % (CONFINE_INSIDE, CONFINE_OUTSIDE)
}))
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
        # In TUI mode the first request is the session-title call and only the second is
        # the prompt, so "todo" answers the first two with the tool call; every other mode
        # is only ever driven one-shot, where the first request is the prompt.
        first_tool_call = _calls["n"] <= (2 if MODE == "todo" else 1)
        if MODE and first_tool_call:
            payload = {
                "tool": TOOL_CALL,
                "write": WRITE_CALL,
                "multi": MULTI_CALL,
                "fail": FAIL_CALL,
                "todo": TODO_CALL,
                "read": READ_CALL,
                "lsp": LSP_CALL,
                "confine": CONFINE_CALL,
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
