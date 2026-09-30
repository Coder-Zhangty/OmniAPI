#!/usr/bin/env python3
"""Evidence-based smoke checks; real Provider keys remain in New API."""
import argparse
from decimal import Decimal, ROUND_HALF_UP
import json
import re
from pathlib import Path
import shutil
import sys
import time
import urllib.request

from gateway import (ROOT, API, Stack, backup, bootstrap, configure_model, credentials,
                     env_read, env_write, init, restore, write_json)

# Plain-text native expression billing: coefficient per million tokens in USD.
BILLING_EXPRESSION = re.compile(
    r"^v1\s*:\s*p\s*\*\s*(\d+(?:\.\d+)?)\s*\+\s*c\s*\*\s*(\d+(?:\.\d+)?)$")


def items(value):
    return value.get("items", []) if isinstance(value, dict) else value


def billing_rates(options, model):
    """Read the live price of one model, so a price change cannot silently pass.

    Only the plain-text shape is accepted. Cache, tool, image or per-request
    pricing would make the expectation below wrong, so it is refused instead of
    being approximated.
    """
    expressions = json.loads(options.get("billing_setting.billing_expr") or "{}")
    expression = expressions.get(model)
    if not isinstance(expression, str):
        raise RuntimeError("no native billing expression for " + model)
    match = BILLING_EXPRESSION.match(expression.strip())
    if not match:
        raise RuntimeError("billing expression is not plain text input/output pricing: " + expression)
    return Decimal(match.group(1)), Decimal(match.group(2))


def expected_quota(usage, rates, quota_per_unit):
    """Quota a plain text request should consume, rounded half up like New API."""
    if not usage:
        return 0
    per_million = Decimal(str(quota_per_unit)) / Decimal(1000000)
    total = (Decimal(usage.get("prompt_tokens", 0)) * rates[0]
             + Decimal(usage.get("completion_tokens", 0)) * rates[1]) * per_million
    return int(total.quantize(Decimal(1), rounding=ROUND_HALF_UP))


def stream(api, key, model, prompt):
    request = urllib.request.Request(api.base + "/v1/chat/completions",
        data=json.dumps({"model": model, "messages": [{"role": "user", "content": prompt}],
                         "stream": True, "stream_options": {"include_usage": True}, "max_tokens": 200}).encode(),
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
    start = time.monotonic()
    events, text, usage, done = [], "", None, False
    with api.opener.open(request, timeout=630) as response:
        if "text/event-stream" not in response.headers.get("Content-Type", ""):
            raise RuntimeError("Response is not SSE")
        request_id = response.headers.get("X-Oneapi-Request-Id", "")
        for raw in response:
            line = raw.decode().strip()
            if not line.startswith("data:"):
                continue
            payload = line[5:].strip()
            if payload == "[DONE]":
                done = True
                break
            data = json.loads(payload)
            if "error" in data:
                raise RuntimeError("SSE returned an error event")
            for choice in data.get("choices", []):
                content = choice.get("delta", {}).get("content", "")
                if content:
                    events.append(round(time.monotonic() - start, 4))
                    text += content
            if data.get("usage"):
                usage = data["usage"]
    end = round(time.monotonic() - start, 4)
    return {"request_id": request_id, "content": text, "content_event_seconds": events,
            "end_seconds": end, "done": done, "usage": usage}


def mock_control(stack, name, body=None):
    code = ("import json,urllib.request; "
            "r=urllib.request.Request('http://127.0.0.1:8000/control',"
            "data=" + ("None" if body is None else repr(json.dumps(body).encode())) + ","
            "headers={'Content-Type':'application/json'}); "
            "print(urllib.request.urlopen(r).read().decode())")
    return json.loads(stack.compose("exec", "-T", name, "python", "-c", code))


def snapshot(stack):
    # Only non-secret persistent columns; compare before/after recreation and restore.
    sql = """SELECT json_build_object(
      'users', (SELECT json_agg(x ORDER BY id) FROM (SELECT id,username,quota,used_quota FROM users) x),
      'tokens', (SELECT json_agg(x ORDER BY id) FROM (SELECT id,user_id,status,remain_quota,used_quota FROM tokens WHERE deleted_at IS NULL) x),
      'channels', (SELECT json_agg(x ORDER BY id) FROM (SELECT id,name,models,priority,weight,status FROM channels) x),
      'logs', (SELECT count(*) FROM logs));"""
    return json.loads(stack.compose("exec", "-T", "database", "psql", "-U", "gateway", "-d", "gateway", "-At", "-c", sql))


def run_checks(stack, model, mock=False, results=None):
    if results is None:
        results = {}
    def record(name, ok, evidence=None):
        results[name] = {"status": "PASS" if ok else "FAIL", "evidence": evidence}
        print(name + ": " + results[name]["status"], flush=True)

    api = API(stack.url)
    admin = API(stack.url)
    admin.login(credentials(stack.root, "admin"))
    user = API(stack.url)
    user.login(credentials(stack.root, "tester"))
    token = credentials(stack.root, "test-token")
    key = token["key"]
    # The expected deduction follows whatever price is live, so the billing
    # checks keep their meaning after the switch from test to retail prices.
    options = admin.options()
    quota_per_unit = options["QuotaPerUnit"]
    try:
        rates = billing_rates(options, model)
        pricing = {"source": "live billing_setting.billing_expr",
                   "usd_per_million": [str(rates[0]), str(rates[1])]}
    except RuntimeError as error:
        rates = None
        pricing = {"source": "unresolved", "error": str(error)}

    def quota_for(usage):
        return expected_quota(usage, rates, quota_per_unit) if rates is not None else None
    with api.opener.open(stack.url, timeout=20) as response:
        record("web_ui", response.status == 200 and b"<html" in response.read().lower())
    record("admin_login", bool(admin.user_id))
    code, catalog, _ = api.request("/v1/models", token=key)
    models = [m["id"] for m in catalog.get("data", [])]
    record("model_catalog", code == 200 and model in models, {"count": len(models), "selected": model})
    write_json(stack.root / "runtime/models.json", {"models": models})

    def counts():
        return [len(mock_control(stack, x)["requests"]) for x in ("mock-a", "mock-b")]

    def balances():
        return (user.ok("/api/user/self")["quota"], user.ok(f"/api/token/{token['id']}")["remain_quota"])

    def await_log(request_id):
        for _ in range(30):
            logs = items(admin.ok("/api/log/?request_id=" + request_id + "&type=2"))
            if logs:
                return logs[0]
            time.sleep(0.2)
        return None

    payload = {"model": model, "messages": [{"role": "user", "content": "Reply exactly with: AI_GATEWAY_OK"}],
               "max_tokens": 32, "stream": False}
    before_counts = counts() if mock else None
    status, _, _ = api.request("/v1/chat/completions", payload, token="sk-invalid-gateway-test")
    record("invalid_token", status in (401, 403), {"http_status": status})
    if mock:
        record("invalid_token_no_upstream", counts() == before_counts)

    before = balances()
    status, response, headers = api.request("/v1/chat/completions", payload, token=key)
    content = response.get("choices", [{}])[0].get("message", {}).get("content", "")
    record("chat_completions", status == 200 and content.strip() == "AI_GATEWAY_OK", {"http_status": status})
    rid = next((v for k, v in headers.items() if k.lower() == "x-oneapi-request-id"), "")
    log = await_log(rid) if rid else None
    usage = response.get("usage", {})
    expected = quota_for(usage)
    after = balances()
    record("usage_and_channel", bool(log) and bool(log.get("channel")) and
           log.get("prompt_tokens") == usage.get("prompt_tokens") and
           log.get("completion_tokens") == usage.get("completion_tokens"), {"request_id": rid})
    record("billing", bool(expected) and log is not None and log["quota"] == expected and
           before[0] - after[0] == expected and before[1] - after[1] == expected,
           {"expected_quota": expected, "user_delta": before[0] - after[0], "token_delta": before[1] - after[1],
            "pricing": pricing})

    before = balances()
    result = stream(api, key, model, "Write the numbers 1 through 80, each on a new line.")
    events = result["content_event_seconds"]
    record("streaming", result["done"] and len(events) >= 2 and events[-1] - events[0] >= 0.05 and events[0] < result["end_seconds"],
           {k: v for k, v in result.items() if k != "content"})
    log = await_log(result["request_id"]) if result["request_id"] else None
    after = balances()
    usage = result["usage"] or {}
    expected = quota_for(usage)
    record("stream_billing", bool(expected) and log is not None and log["quota"] == expected and
           before[0] - after[0] == expected and before[1] - after[1] == expected,
           {"expected_quota": expected, "user_delta": before[0] - after[0], "token_delta": before[1] - after[1],
            "pricing": pricing})

    # Token lifecycle uses a separate disposable token, preserving the main test key.
    name = "lifecycle-" + str(int(time.time()))
    user.ok("/api/token/", {"name": name, "remain_quota": 1000, "expired_time": -1,
                           "unlimited_quota": False, "group": "default"})
    temporary = next(t for t in items(user.ok("/api/token/")) if t["name"] == name)
    temporary_key = user.ok(f"/api/token/{temporary['id']}/key", {})["key"]
    if not temporary_key.startswith("sk-"):
        temporary_key = "sk-" + temporary_key
    user.ok("/api/token/?status_only=true", {"id": temporary["id"], "status": 2}, "PUT")
    before_counts = counts() if mock else None
    status, _, _ = api.request("/v1/chat/completions", payload, token=temporary_key)
    record("disabled_token", status in (401, 403), {"http_status": status})
    user.ok(f"/api/token/{temporary['id']}", method="DELETE")
    status, _, _ = api.request("/v1/chat/completions", payload, token=temporary_key)
    record("deleted_token", status in (401, 403), {"http_status": status})
    if mock:
        record("revoked_tokens_no_upstream", counts() == before_counts)
        record("model_mapping", all(r["model"] == "upstream-a" for r in mock_control(stack, "mock-a")["requests"]))
        admin.option("RetryTimes", "2")
        for failure in ("429", "500", "503", "disconnect"):
            mock_control(stack, "mock-a", {"mode": failure, "reset": True})
            mock_control(stack, "mock-b", {"mode": "ok", "reset": True})
            before = balances()
            code, _, response_headers = api.request("/v1/chat/completions", payload, token=key)
            request_id = next((v for k, v in response_headers.items() if k.lower() == "x-oneapi-request-id"), "")
            if code == 200 and request_id:
                await_log(request_id)
            after = balances()
            # Quota cache updates are asynchronous even with DB batching off.
            deadline = time.monotonic() + 5
            while code == 200 and before[0] - after[0] != 20 and time.monotonic() < deadline:
                time.sleep(0.1)
                after = balances()
            b = mock_control(stack, "mock-b")["requests"]
            record("failover_" + failure, code == 200 and len(b) == 1 and b[0]["model"] == "upstream-b"
                   and before[0] - after[0] == 20, {"requests_per_provider": counts(), "http_status": code,
                   "user_delta": before[0] - after[0], "token_delta": before[1] - after[1], "request_id": request_id})
        for name in ("mock-a", "mock-b"):
            mock_control(stack, name, {"mode": "503", "reset": True})
        before = balances()
        code, _, _ = api.request("/v1/chat/completions", payload, token=key)
        time.sleep(1)
        record("failed_request_refund", code >= 400 and balances() == before)
        for name in ("mock-a", "mock-b"):
            mock_control(stack, name, {"mode": "ok"})
        admin.option("RetryTimes", "0")
    else:
        code, response, _ = api.request("/v1/responses", {"model": model, "input": "Reply exactly with: AI_GATEWAY_OK",
                                                         "max_output_tokens": 32}, token=key)
        record("responses", code == 200 and bool(response.get("output")), {"http_status": code})

    # Inspect logs in memory; store only boolean findings, never matched secret values.
    logs = stack.compose("logs", "--no-color", "nginx")
    secrets_to_check = [key, credentials(stack.root, "admin")["password"],
                        *[stack.env[k] for k in ("DB_PASSWORD", "REDIS_PASSWORD", "SESSION_SECRET", "CRYPTO_SECRET")]]
    if (stack.root / '.secrets/provider-default.json').exists():
        secrets_to_check.append(credentials(stack.root, 'provider-default')['api_key'])
    record("log_secret_scan", not any(s.encode() in logs for s in secrets_to_check))
    record("prompt_body_scan", b"Reply exactly with: AI_GATEWAY_OK" not in logs)
    database_logs = stack.compose("exec", "-T", "database", "psql", "-U", "gateway", "-d", "gateway",
                                  "-At", "-c", "SELECT content || COALESCE(other,'') FROM logs")
    record("database_log_secret_scan", not any(s.encode() in database_logs for s in secrets_to_check))
    record("database_prompt_scan", b"Reply exactly with: AI_GATEWAY_OK" not in database_logs)
    status, denied, _ = user.request("/api/channel/")
    # v0.13.2 admin RBAC returns HTTP 200 with success=false for insufficient role.
    record("user_cannot_read_channels", status in (401, 403) or
           (status == 200 and denied.get("success") is False and not denied.get("data")))
    config = json.loads(stack.compose("config", "--format", "json"))
    record("raw_app_logs_not_persisted", config["services"]["new-api"].get("logging", {}).get("driver") == "none")
    internal = all(not config["services"][name].get("ports") for name in ("new-api", "database", "redis"))
    ports = config["services"]["nginx"]["ports"]
    # Only Nginx may be published, and it must bind the host the operator chose:
    # 127.0.0.1 for a local install, 0.0.0.0 for a public server. Exposure is
    # recorded instead of asserted, so a deliberate public deployment is not a failure.
    host = stack.env.get("GATEWAY_HOST", "127.0.0.1")
    bound = [p.get("host_ip") for p in ports]
    record("port_isolation", internal and bool(bound) and all(ip == host for ip in bound),
           {"gateway_host": host, "nginx_host_ips": bound,
            "exposure": "public" if host not in ("127.0.0.1", "localhost") else "loopback"})
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mock", action="store_true")
    parser.add_argument("--model")
    parser.add_argument("--recovery", action="store_true", help="Drain, recreate containers, backup and restore in an isolated project")
    args = parser.parse_args()
    stack = Stack()
    report_path = ROOT / "runtime" / ("mock-acceptance.json" if args.mock else "real-acceptance.json")
    results = {}
    try:
        if args.mock:
            root = ROOT / "runtime" / ("mock-" + str(int(time.time())))
            root.mkdir(parents=True)
            for file in ("docker-compose.yml", "compose.test.yml", "images.lock.json"):
                shutil.copy2(ROOT / file, root / file)
            for directory in ("nginx", "tests"):
                shutil.copytree(ROOT / directory, root / directory)
            init(root, project=root.name, port=18081)
            values = env_read(root)
            values.update({k: v for k, v in env_read().items() if k.endswith("_IMAGE")})
            env_write(values, root)
            stack = Stack(root, root / "compose.test.yml")
            stack.start()
            admin = bootstrap(stack)
            for name, upstream, priority in (("mock-a", "upstream-a", 30), ("mock-b", "upstream-b", 20)):
                admin.ok("/api/channel/", {"mode": "single", "channel": {"type": 1, "name": name,
                    "key": "synthetic-test-key", "base_url": "http://" + name + ":8000", "group": "default",
                    "models": "public-test-model", "model_mapping": json.dumps({"public-test-model": upstream}),
                    "priority": priority, "weight": 10, "status": 1, "auto_ban": 0}})
            # AddChannel in v0.13.2 commits abilities but does not refresh the
            # Redis-forced in-memory cache immediately. Use the native repair API.
            admin.ok("/api/channel/fix", {})
            args.model = "public-test-model"
            configure_model(stack, args.model)
        if not args.model:
            parser.error("real tests require --model with a verified model name")
        run_checks(stack, args.model, args.mock, results)
        if args.recovery:
            before = snapshot(stack)
            stack.compose("stop", "-t", "620", "nginx")
            stack.compose("up", "-d", "--force-recreate", "--wait", "--wait-timeout", "240", "new-api", "nginx")
            results["persistence"] = {"status": "PASS" if snapshot(stack) == before else "FAIL"}
            dest = backup(stack)
            restored = restore(dest, 18082)
            try:
                results["restore"] = {"status": "PASS" if snapshot(restored) == before else "FAIL"}
                restored_api = API(restored.url)
                restored_api.login(credentials(restored.root, "tester"))
                results["restored_user_login"] = {"status": "PASS"}
            finally:
                restored.compose("stop", "-t", "620")
        else:
            results["persistence"] = results["restore"] = {"status": "NOT TESTED"}
    except Exception as exc:
        # Deliberately do not persist exception bodies from external APIs.
        results["runner"] = {"status": "FAIL", "error_type": type(exc).__name__}
        print("Acceptance failed: " + type(exc).__name__, file=sys.stderr)
    finally:
        write_json(report_path, {"mode": "synthetic" if args.mock else "real", "model": args.model,
                                 "results": results})
        if args.mock and stack.root != ROOT:
            stack.compose("stop", "-t", "620")
    print("Evidence written to " + str(report_path))
    return 1 if any(v["status"] == "FAIL" for v in results.values()) else 0


if __name__ == "__main__":
    sys.exit(main())
