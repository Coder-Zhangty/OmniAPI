#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
mkdir -p certbot/conf certbot/www
chmod 700 certbot/conf
chmod 755 certbot/www

docker compose -f docker-compose.yml -f compose.acme.yml up -d --wait nginx
docker compose -f docker-compose.yml -f compose.acme.yml --profile acme run --rm --no-deps certbot \
  certonly --webroot --webroot-path /var/www/certbot \
  --non-interactive --agree-tos --no-eff-email \
  --email ttyy.zhang@gmail.com \
  --cert-name omniapi.biz -d omniapi.biz -d www.omniapi.biz

docker compose -f docker-compose.yml -f compose.https.yml config --quiet
docker compose -f docker-compose.yml -f compose.https.yml up -d --wait --wait-timeout 300
docker compose -f docker-compose.yml -f compose.https.yml exec -T nginx nginx -t
echo "HTTPS enabled for omniapi.biz and www.omniapi.biz"
