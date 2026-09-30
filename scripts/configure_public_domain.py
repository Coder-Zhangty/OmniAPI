#!/usr/bin/env python3
"""Apply the canonical HTTPS URL and WebAuthn relying-party settings."""
from gateway import API, ROOT, Stack, credentials

DOMAIN = "omniapi.biz"
ORIGIN = "https://omniapi.biz"


def main():
    stack = Stack()
    admin = API(stack.url)
    admin.login(credentials(ROOT, "admin"))
    for key, value in (
        ("ServerAddress", ORIGIN),
        ("passkey.rp_display_name", "OmniAPI"),
        ("passkey.rp_id", DOMAIN),
        ("passkey.origins", ORIGIN),
        ("passkey.allow_insecure_origin", "false"),
    ):
        admin.option(key, value)
    public = API(stack.url).ok("/api/status")
    if public.get("server_address") != ORIGIN or public.get("passkey_rp_id") != DOMAIN:
        raise SystemExit("Public domain settings did not become effective")
    print("Canonical HTTPS and Passkey domain settings applied")


if __name__ == "__main__":
    main()
