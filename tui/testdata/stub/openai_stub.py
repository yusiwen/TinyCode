"""A minimal OpenAI-compatible endpoint, for scenarios that need a model.

It answers any chat completion with one fixed streaming answer, so a scenario can drive
the real request path — provider, SSE parsing, incremental render — with no network, no
key and no cost. Which answer a request gets is decided by MODE and a request counter,
not by the request body, which is what makes it deterministic; the one thing the body is
consulted for is the marker of the TUI's asynchronous session-title call, which must not
consume a position in the script (see TITLE_MARKER below).

Usage: openai_stub.py <port> [mode [cycles [count_file]]]
Started by the scenario that needs it; see prompt-answer.scenario, and
permission-allow-always.scenario for a flow that starts the binary twice and therefore
asks for two cycles.
"""
import http.server
import json
import os
import sys

ANSWER = "stub-answer-ok"       # keep in sync with prompt-answer.scenario
FINAL = "stub-final-ok"         # keep in sync with tool-call.scenario
# "retry" answers the first scripted request with a 429 and the next one with this, so
# the scenario can count what the client drew (see retry-counted.scenario, issue #192).
RETRY_OK = "stub-retry-ok"
# "priced" reports usage and a charge on its final answer, in the chunk order an
# OpenAI-compatible endpoint uses: deltas, finish_reason, then a usage-only chunk whose
# `choices` is empty, then [DONE]. That chunk is why the client reads past finish_reason,
# and the `cost` is what a route reports instead of leaving the client to price it
# (issues #174, #181).
# The finish_reason chunk comes first and the usage chunk after it, which is the order that
# makes a client read past finish_reason at all (issue #174).
PRICED_FINISH = 'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\n'
PRICED_USAGE = (
    'data: {"choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":300,'
    '"total_tokens":1500,"prompt_tokens_details":{"cached_tokens":800},"cost":0.0042}}\n\n'
)
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
# "undo" overwrites a file *inside* the workspace (see undo-command.scenario): the path is
# relative, so it lands in the directory the binary was started in, which the fence
# auto-allows — no approval dialog between the write and the /undo that follows it. The
# content is what the scenario asserts was restored, so it must differ from the bytes the
# wrapper shell seeds.
UNDO_CALL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_u","type":"function",'
    '"function":{"name":"write_file","arguments":"{\\"path\\":\\".stub-undo.txt\\",\\"content\\":\\"stub-undo-after\\"}"}}]}}]}\n\n'
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
# How many times the mode script is served, and where to record what was served.
#
# One tinycode start consumes one cycle in a flow that ends on the model's final answer,
# so a scenario that starts the binary twice needs two (permission-allow-always.scenario).
# The count is explicit because that scenario's wrapper shell checks it: a stub asked for
# two cycles that served one must fail the check loudly instead of answering the second
# start with the final answer, which would let the scenario pass without exercising the
# persisted grant at all (issue #155).
CYCLES = int(sys.argv[3]) if len(sys.argv) > 3 else 1
COUNT_FILE = sys.argv[4] if len(sys.argv) > 4 else ""
# Requests in one cycle: the tool call(s) that make the scenario do something, then the
# final answer that ends the agent loop. "todo" answered two requests with the tool call
# before #155 and keeps that shape.
TOOL_CALLS = 2 if MODE == "todo" else 1
CYCLE = TOOL_CALLS + 1
# The TUI derives a session title with one extra model call after a run's last answer
# (tui/update.go puts this marker into the prompt it builds). That call is asynchronous —
# it may or may not reach the stub before the process exits — so it is answered with the
# final text and counted on its own: were it to consume a cycle position, the request
# after it would be served the wrong half of the script and the scenario would depend on
# a race.
TITLE_MARKER = b"Conversation so far:"
_served = {"requests": 0, "cycles": 0, "title": 0}

# The answer a tool-asking mode gives at the start of each cycle.
SCRIPT = {
    "tool": TOOL_CALL,
    "write": WRITE_CALL,
    "undo": UNDO_CALL,
    "multi": MULTI_CALL,
    "fail": FAIL_CALL,
    "todo": TODO_CALL,
    "read": READ_CALL,
    "lsp": LSP_CALL,
    "confine": CONFINE_CALL,
}


def final_payload(with_usage=False):
    """The streaming answer that ends the loop.

    "retry" and the plain mode answer with ANSWER; a tool mode answers with FINAL.
    with_usage appends the priced usage object, and is passed only for a scripted
    request: the session-title call is answered with the same text but must not report
    tokens or a charge, or the scenario would be racing a call it cannot see.
    """
    if MODE == "retry":
        parts = [RETRY_OK]
    elif MODE in SCRIPT:
        # A tool-asking mode ends its cycle with FINAL; every other mode answers with
        # ANSWER. "priced" is not a tool mode, and answering it with FINAL would make a
        # scenario wait for a marker this mode does not send.
        parts = [FINAL]
    else:
        parts = [ANSWER]
    chunks = [('data: {"choices":[{"delta":{"content":"%s"}}]}\n\n' % part).encode()
              for part in parts]
    if with_usage:
        chunks.append(PRICED_FINISH.encode())
        chunks.append(PRICED_USAGE.encode())
    chunks.append(b"data: [DONE]\n\n")
    return b"".join(chunks)


def record():
    """Write what has been served so far, for the scenario's wrapper shell to read."""
    if not COUNT_FILE:
        return
    with open(COUNT_FILE, "w") as f:
        f.write("cycles=%d requests=%d title=%d\n"
                % (_served["cycles"], _served["requests"], _served["title"]))


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("content-length", 0)))
        if TITLE_MARKER in body:
            # The session-title call: answered, but not part of the mode script.
            _served["title"] += 1
            self.answer(final_payload())
            return

        _served["requests"] += 1
        if MODE == "retry" and _served["requests"] == 1:
            # The transient failure the client is expected to retry: a rate limit with no
            # Retry-After, so the scenario pays one backoff rather than a second.
            self.answer(b"rate limited", status=429)
            return
        # Which position of which cycle this request is. "requests" counts only the
        # requests that belong to the script, so the number a scenario checks is stable
        # whatever the title call does.
        position = (_served["requests"] - 1) % CYCLE
        serving = _served["cycles"] < CYCLES
        if MODE in SCRIPT and serving and position < TOOL_CALLS:
            # The script constants are SSE text; the final answer is built the same way.
            self.answer(SCRIPT[MODE].encode())
            return
        if MODE and serving and position == CYCLE - 1:
            _served["cycles"] += 1
        self.answer(final_payload(with_usage=MODE == "priced"))

    def answer(self, payload, status=200):
        self.send_response(status)
        self.send_header("content-type", "text/event-stream")
        self.send_header("content-length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)
        # Written after every answer, so a wrapper shell can report what was served even
        # when the flow stopped halfway (the failure a scenario has to diagnose).
        record()

    def log_message(self, *args):
        pass


# Readiness: the count file exists before the first request, so a scenario's wrapper
# shell can wait for it instead of sleeping and hoping the server came up.
record()
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
