"""FX snapshots for OmniAPI.

USD is the accounting base. A snapshot is written atomically and carries the
quote time of every currency it holds. Two clocks are tracked separately:

- ``fetched_at`` — when this process pulled the data. Drives the refresh cadence.
- ``as_of`` / ``rate_as_of`` — when the rate was actually published. Drives
  whether that rate may still price an order.

The split matters because the CNY fixing is published once per trading day, so a
Friday rate is legitimately the only one available until Monday. Measuring age
from the *fetch* time would call a valid weekend snapshot stale; measuring from
the *quote* time would refresh the endpoint on every request while the market is
closed. Both clocks are needed.

Sources: ``provider='xe'`` uses the licensed commercial feed; the operator chose
``provider='eastmoney'`` for automatic following, with the decision and its
known caveats recorded in ``config/topup.json`` → ``fx.source_decision``.
"""
import base64
import datetime as dt
import json
import os
import tempfile
import urllib.parse
import urllib.request
from decimal import Decimal
from pathlib import Path

CURRENCIES = ('USD', 'CNY', 'EUR', 'GBP', 'JPY', 'HKD', 'CAD', 'AUD')
# How old a rate may be and still price an order. The CNY fixing is published
# once per trading day, so the Friday rate stays the only available one until
# Monday; 96 hours covers a weekend plus one holiday. This is deliberately not a
# "near real-time" window -- treating a daily fixing as a 15-minute quote would
# reject every order outside Beijing business hours.
MAX_AGE = 345600
# The operator-declared fallback rate in config/topup.json is a recorded
# business decision, not a market quote, so it gets its own (wider) window.
# Past this it stops pricing orders rather than silently charging a stale rate.
FALLBACK_MAX_AGE = 2592000
EASTMONEY_ENDPOINT = 'https://push2.eastmoney.com/api/qt/ulist.np/get'
EASTMONEY_HOSTS = frozenset(('push2.eastmoney.com', 'push2delay.eastmoney.com'))
EASTMONEY_MAX_RESPONSE = 262144
# Recorded by the operator on 2026-09-27; the endpoint is a public web quote
# feed, not a licensed data product. Kept in the snapshot so every quote can be
# traced back to which source was used and on what basis.
EASTMONEY_SOURCE = '东方财富行情（USD 货币对，延迟行情）'
EASTMONEY_LICENSE_STATUS = 'operator-accepted (see config/topup.json fx.source_decision)'
EASTMONEY_SYMBOLS = {
    'CNY': '120.USDCNYC',
    'EUR': '119.USDEUR',
    'GBP': '119.USDGBP',
    'JPY': '119.USDJPY',
    'HKD': '119.USDHKD',
    'CAD': '119.USDCAD',
    'AUD': '119.USDAUD',
}


def utcnow():
    return dt.datetime.now(dt.timezone.utc)


def iso(value=None):
    return (value or utcnow()).isoformat(timespec='seconds')


def parse_time(value):
    if isinstance(value, (int, float)):
        return dt.datetime.fromtimestamp(value, dt.timezone.utc)
    parsed = dt.datetime.fromisoformat(str(value).replace('Z', '+00:00'))
    if parsed.tzinfo is None:
        # A date-only stamp (config's fx_as_of is written as '2026-09-25') parses
        # as naive. Treat it as UTC, otherwise it cannot be subtracted from an
        # aware "now" and every age silently becomes None.
        parsed = parsed.replace(tzinfo=dt.timezone.utc)
    return parsed


def atomic_json(path, payload, mode=0o600):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix=path.name + '.', dir=str(path.parent))
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as handle:
            json.dump(payload, handle, ensure_ascii=False, indent=2)
            handle.write('\n')
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(name, mode)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def _rate_rows(payload):
    """Accept Xe's convert_from response and a simple rates map for tests."""
    if isinstance(payload.get('rates'), dict):
        return payload['rates']
    rows = payload.get('to') or []
    result = {}
    for row in rows:
        code = row.get('quotecurrency') or row.get('currency') or row.get('to')
        value = row.get('mid') if row.get('mid') is not None else row.get('rate')
        if code and value is not None:
            result[str(code).upper()] = value
    return result


def normalize(payload, fetched_at=None):
    rows = _rate_rows(payload)
    rates = {'USD': '1'}
    for code in CURRENCIES[1:]:
        value = Decimal(str(rows.get(code)))
        if not value.is_finite() or value <= 0:
            raise ValueError('invalid Xe rate for ' + code)
        rates[code] = format(value.normalize(), 'f')
    published = payload.get('timestamp') or payload.get('time') or iso(fetched_at)
    try:
        published = iso(parse_time(published))
    except (TypeError, ValueError, OverflowError):
        published = iso(fetched_at)
    return {'base': 'USD', 'rates': rates, 'source': 'Xe Currency Data API',
            'as_of': published, 'fetched_at': iso(fetched_at)}


def fetch_xe(credentials_path, opener=None, now=None):
    credentials = json.loads(Path(credentials_path).read_text(encoding='utf-8'))
    account = str(credentials.get('account_id') or '').strip()
    key = str(credentials.get('api_key') or '').strip()
    if not account or not key:
        raise ValueError('Xe account_id/api_key are required')
    base = str(credentials.get('base_url') or
               'https://xecdapi.xe.com/v1/convert_from.json/').strip()
    query = urllib.parse.urlencode({'from': 'USD', 'to': ','.join(CURRENCIES[1:]),
                                    'amount': '1'})
    request = urllib.request.Request(base + ('&' if '?' in base else '?') + query)
    token = base64.b64encode((account + ':' + key).encode()).decode('ascii')
    request.add_header('Authorization', 'Basic ' + token)
    request.add_header('Accept', 'application/json')
    request.add_header('User-Agent', 'OmniAPI-FX/1.0')
    response = (opener or urllib.request.urlopen)(request, timeout=10)
    payload = json.loads(response.read().decode('utf-8'))
    return normalize(payload, now or utcnow())


def normalize_eastmoney(payload, fetched_at=None):
    """Normalize the seven public Eastmoney USD pairs into one USD snapshot."""
    rows = ((payload.get('data') or {}).get('diff') or [])
    if not isinstance(rows, list) or len(rows) > 64:
        raise ValueError('invalid Eastmoney response shape')
    by_code = {str(row.get('f12') or '').upper(): row for row in rows}
    rates = {'USD': '1'}
    rate_times = {}
    timestamps = []
    for currency, secid in EASTMONEY_SYMBOLS.items():
        code = secid.split('.', 1)[1]
        row = by_code.get(code)
        if not row:
            raise ValueError('missing Eastmoney symbol ' + code)
        value = Decimal(str(row.get('f2')))
        if not value.is_finite() or value <= 0:
            raise ValueError('invalid Eastmoney rate for ' + currency)
        rates[currency] = format(value.normalize(), 'f')
        # A pair without a usable quote time is kept out of the age calculation
        # rather than silently dated 1970, so it cannot outvote a real quote.
        stamp = row.get('f124')
        if stamp:
            parsed = parse_time(stamp)
            timestamps.append(parsed)
            rate_times[currency] = iso(parsed)
    if not timestamps:
        raise ValueError('Eastmoney response carried no quote times')
    return {
        'base': 'USD',
        'rates': rates,
        'source': EASTMONEY_SOURCE,
        'as_of': iso(max(timestamps)),
        'fetched_at': iso(fetched_at),
        'rate_as_of': rate_times,
        'license_status': EASTMONEY_LICENSE_STATUS,
    }


def fetch_eastmoney(opener=None, now=None):
    query = urllib.parse.urlencode({
        'fltt': '2',
        'invt': '2',
        'fields': 'f1,f2,f12,f13,f14,f124',
        'secids': ','.join(EASTMONEY_SYMBOLS.values()),
    })
    request = urllib.request.Request(EASTMONEY_ENDPOINT + '?' + query)
    request.add_header('Accept', 'application/json')
    request.add_header('User-Agent', 'OmniAPI-FX-Evaluation/1.0')
    response = (opener or urllib.request.urlopen)(request, timeout=15)
    final_url = response.geturl() if hasattr(response, 'geturl') else EASTMONEY_ENDPOINT
    parsed = urllib.parse.urlsplit(final_url)
    if parsed.scheme != 'https' or (parsed.hostname or '').lower() not in EASTMONEY_HOSTS:
        raise ValueError('Eastmoney redirected to an untrusted host')
    content_type = response.headers.get_content_type() if hasattr(response, 'headers') else None
    if content_type and content_type not in ('application/json', 'text/json', 'text/plain'):
        raise ValueError('unexpected Eastmoney content type')
    raw = response.read(EASTMONEY_MAX_RESPONSE + 1)
    if len(raw) > EASTMONEY_MAX_RESPONSE:
        raise ValueError('Eastmoney response is too large')
    payload = json.loads(raw.decode('utf-8'))
    if payload.get('rc') != 0:
        raise ValueError('Eastmoney quote request failed')
    return normalize_eastmoney(payload, now or utcnow())


def fetch(credentials_path=None, opener=None, now=None, provider='xe'):
    provider = str(provider or 'xe').lower()
    if provider == 'xe':
        return fetch_xe(credentials_path, opener=opener, now=now)
    if provider == 'eastmoney':
        return fetch_eastmoney(opener=opener, now=now)
    raise ValueError('unsupported FX provider ' + provider)


def age_seconds(stamp, now=None):
    """Seconds since `stamp`, or None when the stamp is absent/unreadable."""
    if not stamp:
        return None
    try:
        delta = (now or utcnow()) - parse_time(stamp)
    except (TypeError, ValueError, OverflowError):
        return None
    return max(0, int(delta.total_seconds()))


def rate_as_of(snapshot, currency):
    """When `currency` was quoted, falling back to the snapshot's own time."""
    per_currency = (snapshot.get('rate_as_of') or {}).get(currency)
    return per_currency or snapshot.get('as_of')


def evaluate(snapshot, currency='CNY', now=None, max_age=None):
    """Decide whether a snapshot may still price `currency`, and say why not."""
    limit = int(max_age if max_age is not None else
                (snapshot.get('max_age_seconds') or MAX_AGE))
    published = rate_as_of(snapshot, currency)
    age = age_seconds(published, now)
    raw = (snapshot.get('rates') or {}).get(currency)
    quoted = False
    if raw is not None:
        try:
            quoted = Decimal(str(raw)) > 0
        except (TypeError, ValueError, ArithmeticError):
            quoted = False
    usable = quoted and age is not None and age <= limit
    if usable:
        reason = None
    elif not quoted:
        reason = 'rate_missing'
    elif age is None:
        reason = 'quote_time_unknown'
    else:
        reason = 'quote_too_old'
    return {'currency': currency, 'rate': raw if quoted else None, 'as_of': published,
            'age_seconds': age, 'max_age_seconds': limit, 'usable': usable,
            'reason': reason}


def unavailable(reason='unavailable', limit=None):
    """The shape returned when no snapshot can be read at all."""
    return {'base': 'USD', 'rates': {'USD': '1'}, 'source': reason,
            'as_of': None, 'fetched_at': None, 'stale': True, 'age_seconds': None,
            'fetched_age_seconds': None, 'max_age_seconds': int(limit or MAX_AGE)}


def load(cache_path, now=None, max_age=None):
    """Read the snapshot. An unreadable or corrupt cache is 'unavailable', not a crash.

    The cache is written by whichever process ran the refresh (the preheat timer,
    or an operator running --refresh-fx by hand) and read by the service account.
    A mode/ownership mismatch must therefore degrade into the labelled fallback
    path, never into a 500 on the checkout page.
    """
    path = Path(cache_path)
    limit = int(max_age or MAX_AGE)
    try:
        raw = path.read_text(encoding='utf-8')
    except OSError:
        return unavailable('unavailable', limit)
    try:
        snapshot = json.loads(raw)
    except ValueError:
        return unavailable('unreadable snapshot', limit)
    if not isinstance(snapshot, dict):
        return unavailable('unreadable snapshot', limit)
    snapshot['max_age_seconds'] = limit
    snapshot['age_seconds'] = age_seconds(snapshot.get('as_of'), now)
    snapshot['fetched_age_seconds'] = age_seconds(snapshot.get('fetched_at'), now)
    snapshot['stale'] = snapshot['age_seconds'] is None or snapshot['age_seconds'] > limit
    return snapshot


def needs_refresh(snapshot, refresh_seconds, now=None):
    """Refresh on the *fetch* clock, so a closed market does not hammer the feed.

    Falls back to deriving the age from ``fetched_at`` so the helper is correct
    for a raw snapshot dict as well as one returned by :func:`load`.
    """
    age = snapshot.get('fetched_age_seconds')
    if age is None:
        age = age_seconds(snapshot.get('fetched_at'), now)
    if age is None:
        return True
    return age >= int(refresh_seconds)


def refresh(credentials_path, cache_path, opener=None, now=None, provider='xe',
            max_age=None):
    snapshot = fetch(credentials_path, opener=opener, now=now, provider=provider)
    atomic_json(cache_path, snapshot)
    return load(cache_path, now=now, max_age=max_age)
