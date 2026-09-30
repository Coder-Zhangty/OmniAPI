#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
docker compose -f docker-compose.yml -f compose.https.yml --profile acme run --rm --no-deps certbot renew --quiet --no-random-sleep-on-renew
docker compose -f docker-compose.yml -f compose.https.yml exec -T nginx nginx -t
docker compose -f docker-compose.yml -f compose.https.yml exec -T nginx nginx -s reload
echo "Certificate renewal check and Nginx reload completed"
