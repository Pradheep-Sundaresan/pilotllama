"""Server-side standard-library example; never expose the token to a browser."""

from __future__ import annotations

import http.client
import json
import os
import sys
from urllib.parse import urlsplit


BASE_URL = os.environ.get("PILOTLLAMA_BASE_URL", "http://127.0.0.1:8080/v1")
API_KEY = os.environ.get("PILOTLLAMA_API_KEY")
if not API_KEY:
    raise SystemExit("PILOTLLAMA_API_KEY is required")


def request(method: str, path: str, body: object | None = None) -> http.client.HTTPResponse:
    base = urlsplit(BASE_URL)
    connection = http.client.HTTPConnection(base.hostname, base.port, timeout=120)
    payload = None if body is None else json.dumps(body).encode()
    headers = {"Authorization": f"Bearer {API_KEY}"}
    if payload is not None:
        headers["Content-Type"] = "application/json"
    connection.request(method, f"{base.path}{path}", payload, headers)
    return connection.getresponse()


models = request("GET", "/models")
if models.status != 200:
    raise SystemExit(f"model discovery failed: {models.status} {models.read().decode()}")
model = os.environ.get("PILOTLLAMA_MODEL") or json.loads(models.read())["data"][0]["id"]

response = request(
    "POST",
    "/chat/completions",
    {
        "model": model,
        "messages": [{"role": "user", "content": "Explain continuous batching simply."}],
        "max_tokens": 256,
        "temperature": 0.7,
        "stream": True,
        "pilotllama": {"include_metrics": True},
    },
)
if response.status != 200:
    retry = response.getheader("Retry-After")
    raise SystemExit(f"PilotLlama {response.status}; retry={retry}: {response.read().decode()}")

finished = False
while line := response.readline():
    if not line.startswith(b"data: "):
        continue
    data = line[6:].strip()
    if data == b"[DONE]":
        if not finished:
            raise RuntimeError("stream ended without a successful finish chunk")
        break
    event = json.loads(data)
    if "error" in event:
        raise RuntimeError(f'{event["error"]["code"]}: {event["error"]["message"]}')
    if event.get("object") == "pilotllama.metrics":
        print("\nmetrics", json.dumps(event["metrics"], indent=2), file=sys.stderr)
        continue
    choice = event["choices"][0]
    reasoning = choice["delta"].get("reasoning_content")
    content = choice["delta"].get("content")
    if reasoning:
        print(reasoning, end="", file=sys.stderr, flush=True)
    if content:
        print(content, end="", flush=True)
    if choice.get("finish_reason"):
        finished = True
else:
    raise RuntimeError("stream ended without [DONE]")
