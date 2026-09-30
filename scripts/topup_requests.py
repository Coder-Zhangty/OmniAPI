"""Recharge submissions: the queue that tells the operator a user paid.

Recharge here is manual by design. The user scans the operator's QR code, the
operator confirms the money arrived in their own collection channel, and only
then is credit issued. What was missing is the first half: nothing told the
operator that a user had paid, so the user had to email them and hope.

This module keeps that half. It is a queue, not a payment callback - a
submission is the user's claim, never evidence that money arrived.

Design:
- Append-only event log (runtime/topup-requests.jsonl). Current state is derived
  by folding events, so a status change never rewrites history and the file
  stays a usable audit trail.
- The browser proposes only a user id and an amount. The USD value and the quota
  are recomputed here with the same helpers the recharge page and the credit
  ledger use, so a tampered submission cannot invent quota.
- Money is Decimal end to end; a float never reaches the ledger.
"""
import argparse
import datetime as dt
import json
import os
import re
import secrets
import sys
import time
from collections import defaultdict, deque
from decimal import Decimal, InvalidOperation, ROUND_HALF_UP
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from commercial_quote import quota_from_usd, usd_from_cny  # noqa: E402
from gateway import ROOT, write_json  # noqa: E402

DEFAULT_EVENTS = ROOT / 'runtime/topup-requests.jsonl'
DEFAULT_QUOTES = ROOT / 'runtime/topup-quotes.jsonl'
DEFAULT_CONFIG = ROOT / 'config/topup.json'
REQUEST_ID = re.compile(r'^T-\d{8}-[0-9a-f]{6}$')
QUOTE_ID = re.compile(r'^Q-\d{8}-[0-9a-f]{8}$')
# Money arrives as JSON, so it is either a plain number or a plain numeric
# string. Exponent notation is refused: '1e9' hides the magnitude from a human
# reading the queue later, and a money field should never need it.
PLAIN_AMOUNT = re.compile(r'^\d{1,9}(?:\.\d{1,18})?$')
MAX_AMOUNT_CNY = Decimal('100000')
CENT = Decimal('0.01')
OPEN_STATES = ('pending',)
CLOSED_STATES = ('fulfilled', 'dismissed')


def plain(value):
    """Render a Decimal without an exponent, e.g. Decimal('20.00') -> '20'."""
    return format(Decimal(str(value)).normalize(), 'f')


def new_id(stamp=None, token=None):
    """Short, sortable request id. The random tail avoids a same-second clash."""
    stamp = stamp or dt.datetime.now(dt.timezone.utc)
    return 'T-%s-%s' % (stamp.strftime('%Y%m%d'), (token or secrets.token_hex(3))[:6])


def new_quote_id(stamp=None, token=None):
    stamp = stamp or dt.datetime.now(dt.timezone.utc)
    return 'Q-%s-%s' % (stamp.strftime('%Y%m%d'), (token or secrets.token_hex(4))[:8])


def allowed_states():
    return OPEN_STATES + CLOSED_STATES


def load_events(path=DEFAULT_EVENTS):
    """Read the append-only log. A truncated last line is not fatal."""
    path = Path(path)
    if not path.exists():
        return []
    events = []
    for line in path.read_text(encoding='utf-8').splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if isinstance(event, dict):
            events.append(event)
    return events


def append_event(event, path=DEFAULT_EVENTS):
    """Append one event and flush it to disk before returning.

    A single O_APPEND write keeps concurrent writers from interleaving, and the
    fsync means an acknowledged submission survives a power loss.
    """
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    line = json.dumps(event, ensure_ascii=False, sort_keys=True) + '\n'
    with path.open('a', encoding='utf-8', newline='\n') as handle:
        handle.write(line)
        handle.flush()
        os.fsync(handle.fileno())
    return event


def fold(events):
    """Derive the current list of requests from the log, oldest first.

    A later event for an unknown request id is ignored rather than resurrected:
    a status can only ever apply to a submission that was actually recorded.
    """
    records = {}
    order = []
    for event in events:
        kind = event.get('type')
        request_id = str(event.get('id') or '')
        if not request_id:
            continue
        if kind == 'request':
            if request_id in records:
                continue
            order.append(request_id)
            record = dict(event)
            record['status'] = 'pending'
            record['handled_at'] = None
            record['transaction'] = None
            record['code'] = None
            record['handled_by'] = None
            record['escalated_at'] = None
            records[request_id] = record
        elif kind == 'escalated' and request_id in records:
            record = records[request_id]
            if record['status'] == 'pending' and not record.get('escalated_at'):
                record['escalated_at'] = event.get('at')
        elif kind in CLOSED_STATES and request_id in records:
            record = records[request_id]
            if record['status'] in CLOSED_STATES:
                continue
            record['status'] = kind
            record['handled_at'] = event.get('at')
            record['transaction'] = event.get('transaction')
            record['code'] = event.get('code')
            record['handled_by'] = event.get('by')
            record['note'] = event.get('note')
            for key in ('net_received', 'credited_usd', 'reference'):
                if key in event:
                    record[key] = event.get(key)
    return [{k: v for k, v in records[request_id].items() if k != 'type'} for request_id in order]


def requests(path=DEFAULT_EVENTS, status=None):
    rows = fold(load_events(path))
    if status is None:
        return rows
    wanted = (status,) if isinstance(status, str) else tuple(status)
    return [row for row in rows if row.get('status') in wanted]


def pending(path=DEFAULT_EVENTS):
    return requests(path, OPEN_STATES)


def counts(path=DEFAULT_EVENTS):
    tally = {state: 0 for state in allowed_states()}
    for row in fold(load_events(path)):
        tally[row.get('status')] = tally.get(row.get('status'), 0) + 1
    return tally


def find(path, request_id):
    key = str(request_id or '').strip()
    return next((row for row in fold(load_events(path)) if row['id'] == key), None)


def decimal_amount(value):
    """Accept only a plain, finite, positive amount; returns a 2dp Decimal."""
    if value is None or isinstance(value, bool):
        raise ValueError('金额无效')
    if isinstance(value, str):
        text = value.strip()
    elif isinstance(value, (int, float, Decimal)):
        text = str(value)
    else:
        raise ValueError('金额无效')
    if not text or len(text) > 24 or not PLAIN_AMOUNT.match(text):
        raise ValueError('金额无效')
    try:
        amount = Decimal(text)
    except InvalidOperation:
        raise ValueError('金额无效')
    if not amount.is_finite() or amount <= 0:
        raise ValueError('金额无效')
    return amount.quantize(CENT, rounding=ROUND_HALF_UP)


def load_quotes(path=DEFAULT_QUOTES):
    return [row for row in load_events(path) if row.get('type') == 'quote']


def find_quote(path, quote_id):
    key = str(quote_id or '').strip()
    return next((row for row in reversed(load_quotes(path)) if row.get('id') == key), None)


def create_quote(method, amount, currency, usd, rate, source, as_of,
                 quotes_path=DEFAULT_QUOTES, now=None, ttl=900, note=None):
    stamp = now or dt.datetime.now(dt.timezone.utc)
    amount = decimal_amount(amount)
    usd = Decimal(str(usd)).quantize(Decimal('0.000001'), rounding=ROUND_HALF_UP)
    if usd <= 0:
        raise ValueError('报价额度无效')
    quote = {'type': 'quote', 'id': new_quote_id(stamp), 'method': str(method),
             'currency': str(currency).upper(), 'quoted_amount': plain(amount),
             'usd': plain(usd), 'fx_rate': plain(rate), 'fx_source': str(source),
             'fx_as_of': as_of, 'created_at': stamp.isoformat(timespec='seconds'),
             'expires_at': (stamp + dt.timedelta(seconds=int(ttl))).isoformat(timespec='seconds')}
    if note:
        # Set only when the fallback rate was used, so a normal quote never
        # carries a warning the customer did not need to read.
        quote['fx_note'] = str(note)
    append_event(quote, quotes_path)
    return quote


def quote_valid(quote, now=None):
    if not quote:
        return False
    try:
        expires = dt.datetime.fromisoformat(str(quote['expires_at']))
        stamp = now or dt.datetime.now(dt.timezone.utc)
        if expires.tzinfo is None:
            expires = expires.replace(tzinfo=dt.timezone.utc)
        return stamp <= expires
    except (KeyError, TypeError, ValueError):
        return False


def reference_used(events_path, reference):
    key = str(reference or '').strip().casefold()
    if not key:
        return False
    return any(str(row.get('reference') or '').strip().casefold() == key
               for row in fold(load_events(events_path)))


def build_request(config, user_id, amount, source='web', client='', now=None):
    """Turn a validated claim into a request event with server-side amounts."""
    rate = config.get('cny_per_usd')
    if not rate:
        raise ValueError('config cny_per_usd is missing; the rate must come from the operator')
    amount = decimal_amount(amount)
    usd = usd_from_cny(amount, rate)
    quota = quota_from_usd(usd)
    if quota <= 0:
        raise ValueError('derived quota is not positive; check the amount and rate')
    stamp = now or dt.datetime.now(dt.timezone.utc)
    return {
        'type': 'request',
        'id': new_id(stamp),
        'uid': int(user_id),
        'cny': plain(amount),
        'usd': plain(usd),
        'quota': quota,
        'rate_cny_per_usd': plain(rate),
        'source': str(source),
        'client': str(client)[:64],
        'created_at': stamp.isoformat(timespec='seconds'),
    }


def build_quoted_request(user_id, quote, reference='', source='web', client='', now=None):
    stamp = now or dt.datetime.now(dt.timezone.utc)
    event = {'type': 'request', 'id': new_id(stamp), 'uid': int(user_id),
             'quote_id': quote['id'], 'method': quote['method'],
             'currency': quote['currency'], 'quoted_amount': quote['quoted_amount'],
             'usd': quote['usd'], 'quota': quota_from_usd(quote['usd']),
             'fx_rate': quote['fx_rate'], 'fx_source': quote['fx_source'],
             'fx_as_of': quote['fx_as_of'], 'quote_expires_at': quote['expires_at'],
             'reference': str(reference or '').strip(), 'source': str(source),
             'client': str(client)[:64], 'created_at': stamp.isoformat(timespec='seconds')}
    if quote.get('fx_note'):
        event['fx_note'] = quote['fx_note']
    if event['method'].startswith('crypto:'):
        _, asset, network = (event['method'].split(':', 2) + ['', ''])[:3]
        event.update(asset=asset.upper(), network=network)
    return event


def close_request(request_id, status, path=DEFAULT_EVENTS, code=None, transaction=None,
                  operator='admin', note=None, now=None, net_received=None,
                  credited_usd=None, reference=None):
    """Record a terminal event. Refuses a request that is missing or already closed."""
    if status not in CLOSED_STATES:
        raise ValueError('status must be one of ' + ', '.join(CLOSED_STATES))
    record = find(path, request_id)
    if record is None:
        raise ValueError('申请不存在：' + str(request_id))
    if record['status'] in CLOSED_STATES:
        raise ValueError('申请已处理（%s）：%s' % (record['status'], record['id']))
    stamp = now or dt.datetime.now(dt.timezone.utc)
    event = {'type': status, 'id': record['id'], 'at': stamp.isoformat(timespec='seconds'),
             'by': str(operator)}
    if code:
        event['code'] = code
    if transaction:
        event['transaction'] = str(transaction)
    if note:
        event['note'] = str(note)
    if net_received is not None:
        event['net_received'] = plain(net_received)
    if credited_usd is not None:
        event['credited_usd'] = plain(credited_usd)
    if reference:
        event['reference'] = str(reference)
    return append_event(event, path)


class SlidingWindow:
    """In-process rate gate. The queue is the durable record; this is the door."""

    def __init__(self, limit, window, clock=time.monotonic, max_keys=4096):
        self.limit = int(limit)
        self.window = float(window)
        self.clock = clock
        self.max_keys = int(max_keys)
        self._hits = {}

    def _trim(self, key, now):
        hits = self._hits.get(key)
        if hits is None:
            hits = self._hits[key] = deque()
        while hits and now - hits[0] > self.window:
            hits.popleft()
        return hits

    def _sweep(self):
        # An open endpoint sees many source addresses; drop the ones with no
        # recent activity so memory cannot grow without bound.
        if len(self._hits) <= self.max_keys:
            return
        now = self.clock()
        for key in [k for k, hits in self._hits.items() if not hits or now - hits[-1] > self.window]:
            self._hits.pop(key, None)

    def allow(self, key):
        now = self.clock()
        hits = self._trim(key, now)
        if len(hits) >= self.limit:
            return False
        hits.append(now)
        self._sweep()
        return True

    def retry_after(self, key):
        now = self.clock()
        hits = self._trim(key, now)
        if len(hits) < self.limit:
            return 0
        return max(1, int(self.window - (now - hits[0])) + 1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--events', type=Path, default=DEFAULT_EVENTS)
    parser.add_argument('--pending', action='store_true', help='只列待核验的申请')
    parser.add_argument('--all', action='store_true', help='列出全部申请')
    parser.add_argument('--dismiss', help='忽略一笔申请（不发放额度）')
    parser.add_argument('--reason', default='operator dismissed', help='与 --dismiss 一起记录')
    parser.add_argument('--apply', action='store_true', help='--dismiss 真正写入；不加则只预演')
    args = parser.parse_args()

    if args.dismiss:
        record = find(args.events, args.dismiss)
        if record is None:
            parser.exit(2, 'BLOCKED: 申请不存在：' + args.dismiss + '\n')
        if not args.apply:
            result = {'status': 'DRY_RUN', 'would_dismiss': record, 'events_written': False}
        else:
            event = close_request(args.dismiss, 'dismissed', args.events,
                                  operator='cli', note=args.reason)
            result = {'status': 'PASS', 'dismissed': args.dismiss, 'event': event}
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return

    rows = requests(args.events, None if args.all else OPEN_STATES)
    write_json(ROOT / 'runtime/topup-requests-summary.json',
               {'counts': counts(args.events), 'shown': rows,
                'generated_at': dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds')})
    print(json.dumps({'counts': counts(args.events), 'requests': rows},
                     ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
