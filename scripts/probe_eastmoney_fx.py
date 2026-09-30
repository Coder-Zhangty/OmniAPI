#!/usr/bin/env python3
"""Read-only Eastmoney FX adapter probe; never writes the production cache."""

import json

import fx_rates


def main():
    snapshot = fx_rates.fetch(provider='eastmoney')
    age = max(0, int((fx_rates.utcnow() - fx_rates.parse_time(snapshot['as_of'])).total_seconds()))
    snapshot['age_seconds'] = age
    snapshot['stale'] = age > fx_rates.MAX_AGE
    print(json.dumps(snapshot, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
