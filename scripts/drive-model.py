#!/usr/bin/env python3
"""Bounded, opt-in local GGUF compatibility drive. Standard library only."""
import argparse, json, os, secrets, signal, socket, subprocess, sys, tempfile, time, urllib.error, urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
STATE = ROOT / "build" / "pilotllama"

def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0)); return sock.getsockname()[1]

def request(base, path, token="", data=None, timeout=60):
    body = None if data is None else json.dumps(data).encode()
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    if body is not None: headers["Content-Type"] = "application/json"
    with urllib.request.urlopen(urllib.request.Request(base + path, body, headers), timeout=timeout) as response:
        return json.load(response)

def main():
    parser = argparse.ArgumentParser(description="Run a bounded PilotLlama model Driving Test")
    parser.add_argument("model", type=Path); parser.add_argument("--model-id", default="driving-test-model")
    parser.add_argument("--draft-model", type=Path, help="optional existing DFlash GGUF for qualification")
    parser.add_argument("--context-tokens", type=int, default=4096); parser.add_argument("--max-output-tokens", type=int, default=256)
    args = parser.parse_args()
    model = args.model.resolve(); draft = args.draft_model.resolve() if args.draft_model else None; api = ROOT / "build" / "inferno-api"; backend = ROOT / "build" / "grpc" / "inferno_rpc_server"
    if not model.is_file() or not api.is_file() or not backend.is_file(): parser.error("model and built PilotLlama binaries must exist")
    if draft is not None and not draft.is_file(): parser.error("draft model must be an existing file")
    STATE.mkdir(parents=True, exist_ok=True); os.chmod(STATE, 0o700)
    token, listen = secrets.token_urlsafe(32), port(); base = f"http://127.0.0.1:{listen}"
    checks, capabilities, memory = {}, {}, None
    with tempfile.TemporaryDirectory(prefix="driving-", dir=STATE) as temporary:
        env = os.environ.copy(); env["PILOTLLAMA_DRIVE_TOKEN"] = token
        command = [str(api), "--listen", f"127.0.0.1:{listen}", "--backend-exec", str(backend), "--backend-state-dir", temporary,
                   "--model", str(model), "--model-id", args.model_id, "--backend-batching", "queue",
                   "--context-tokens", str(args.context_tokens), "--max-output-tokens", str(args.max_output_tokens),
                   "--auth-token-env", "PILOTLLAMA_DRIVE_TOKEN"]
        if draft is not None: command.extend(["--draft-model", str(draft)])
        process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            deadline = time.monotonic() + 120
            while time.monotonic() < deadline:
                if process.poll() is not None: raise RuntimeError("isolated PilotLlama failed to start")
                try:
                    request(base, "/readyz", timeout=1); break
                except Exception: time.sleep(.2)
            else: raise RuntimeError("isolated PilotLlama readiness timed out")
            capabilities = request(base, "/internal/v1/model-capabilities", token)
            def chat(messages, **extra):
                payload = {"model": args.model_id, "messages": messages, "max_tokens": 64, "temperature": 0, **extra}
                return request(base, "/v1/chat/completions", token, payload)["choices"][0]
            try: checks["basic_generation"] = bool(chat([{"role":"user","content":"Reply briefly with OK."}])["message"]["content"])
            except Exception: checks["basic_generation"] = False
            support = capabilities.get("capabilities", {})
            if support.get("multi_turn") in ("detected", "verified"):
                try: checks["multi_turn"] = bool(chat([{"role":"user","content":"Remember ORANGE."},{"role":"assistant","content":"Remembered."},{"role":"user","content":"What word?"}])["message"]["content"])
                except Exception: checks["multi_turn"] = False
            try:
                payload = [{"role":"user","content":"Give one short greeting."}]
                a = chat(payload, temperature=.7, seed=42)["message"]["content"]; b = chat(payload, temperature=.7, seed=42)["message"]["content"]
                checks["seeded_sampling"] = a == b
            except Exception: checks["seeded_sampling"] = False
            if capabilities.get("has_embedded_chat_template"):
                try:
                    choice = chat([{"role":"user","content":"Return answer 4."}], max_tokens=128, response_format={"type":"json_schema","json_schema":{"name":"answer","strict":True,"schema":{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":False}}})
                    checks["structured_json"] = isinstance(json.loads(choice["message"]["content"]), dict)
                except Exception: checks["structured_json"] = False
            if support.get("tools") in ("detected", "verified"):
                try:
                    choice = chat([{"role":"user","content":"Use weather for Chennai."}], tools=[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}], tool_choice="required")
                    checks["tools"] = bool(choice["message"].get("tool_calls"))
                except Exception: checks["tools"] = False
            try: memory = request(base, "/internal/v1/memory", token)
            except Exception: memory = None
            history = request(base, "/internal/v1/requests", token).get("records", []) if draft is not None else []
        finally:
            if process.poll() is None: process.send_signal(signal.SIGTERM)
            try: process.wait(timeout=20)
            except subprocess.TimeoutExpired: process.kill(); process.wait()
    passed, total = sum(value is True for value in checks.values()), len(checks)
    grade = "verified" if total and passed == total else ("compatible" if checks.get("basic_generation") else "experimental")
    stat = model.stat(); receipt = STATE / "continuous-qualified"; continuous = False
    if receipt.is_file():
        lines = receipt.read_text().splitlines()
        continuous = len(lines) >= 2 and lines[0] == str(model) and lines[1] == f"{stat.st_size}:{int(stat.st_mtime)}"
        if draft is not None:
            draft_stat = draft.stat()
            continuous = continuous and len(lines) >= 4 and lines[2] == str(draft) and lines[3] == f"{draft_stat.st_size}:{int(draft_stat.st_mtime)}"
    try: physical_memory = int(subprocess.check_output(["/usr/sbin/sysctl", "-n", "hw.memsize"], text=True).strip())
    except Exception: physical_memory = None
    speculative = None
    if draft is not None:
        native = [row.get("native") or {} for row in history]
        speculative = {"draft_file_name":draft.name,"draft_file_size_bytes":draft.stat().st_size,
                       "drafted_tokens":sum(row.get("speculative_drafted_tokens") or 0 for row in native),
                       "accepted_tokens":sum(row.get("speculative_accepted_tokens") or 0 for row in native)}
        speculative["acceptance_rate"] = speculative["accepted_tokens"] / speculative["drafted_tokens"] if speculative["drafted_tokens"] else None
    report = {"schema_version":1,"observed_at_unix_ms":int(time.time()*1000),"model_id":args.model_id,"file_name":model.name,
              "file_size_bytes":stat.st_size,"file_mtime_unix":int(stat.st_mtime),"grade":grade,"checks":checks,
              "continuous_qualified":continuous,"physical_memory_bytes":physical_memory,"capabilities":capabilities,"memory":memory,
              "speculative_decoding":speculative,"method":"bounded_local_driving_test"}
    target = STATE / "driving-test.json"
    with tempfile.NamedTemporaryFile("w", dir=STATE, prefix="driving-test.", delete=False) as handle:
        json.dump(report, handle, indent=2, sort_keys=True); handle.write("\n"); temporary = Path(handle.name)
    os.chmod(temporary, 0o600); temporary.replace(target)
    print(json.dumps({"grade":grade,"passed":passed,"total":total,"report":str(target)}))
    return 0 if checks.get("basic_generation") else 1

if __name__ == "__main__": sys.exit(main())
