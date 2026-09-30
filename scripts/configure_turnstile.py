#!/usr/bin/env python3
"""Configure native New API Turnstile without exposing the secret in argv/logs."""
import argparse
import getpass
import json
import re
from pathlib import Path

from gateway import API, ROOT, Stack, credentials, write_json


def read_keys(path):
    """Read JSON or the labelled two-line export without printing secrets."""
    raw = Path(path).read_text(encoding="utf-8-sig")
    try:
        data = json.loads(raw)
        return str(data.get("site_key", "")).strip(), str(data.get("secret_key", "")).strip()
    except json.JSONDecodeError:
        pass
    values = {}
    for line in raw.splitlines():
        match = re.match(r"\s*(Site|Secret)\s*key\s*[:：]\s*(\S+)\s*$", line, re.I)
        if match:
            values[match.group(1).lower()] = match.group(2)
    return values.get("site", ""), values.get("secret", "")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--disable", action="store_true")
    parser.add_argument("--file", type=Path,
                        help="read keys from a root-only JSON or labelled text file")
    args = parser.parse_args()
    stack = Stack()
    admin = API(stack.url)
    admin.login(credentials(ROOT, "admin"))
    secret_file = ROOT / ".secrets" / "turnstile.json"

    if args.disable:
        admin.option("TurnstileCheckEnabled", "false")
        print("Turnstile disabled; stored keys were left intact for rollback")
        return

    if args.file:
        site_key, secret_key = read_keys(args.file)
    else:
        site_key = input("Turnstile Site Key: ").strip()
        secret_key = getpass.getpass("Turnstile Secret Key (hidden): ").strip()
    if len(site_key) < 20 or len(secret_key) < 20:
        raise SystemExit("Turnstile keys appear incomplete")
    write_json(secret_file, {"site_key": site_key, "secret_key": secret_key}, private=True)
    admin.option("TurnstileSiteKey", site_key)
    admin.option("TurnstileSecretKey", secret_key)
    admin.option("TurnstileCheckEnabled", "true")

    public = API(stack.url).ok("/api/status")
    if not public.get("turnstile_check") or public.get("turnstile_site_key") != site_key:
        raise SystemExit("Public Turnstile status did not match the applied configuration")
    if any("secret" in key.lower() for key in public):
        raise SystemExit("Public status unexpectedly exposed a secret-named field")
    print("Turnstile enabled; secret omitted")


if __name__ == "__main__":
    main()
