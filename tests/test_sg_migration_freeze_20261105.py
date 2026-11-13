"""Local-only orchestration safety tests. No network, subprocess or remote host."""
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

try:
    import fcntl
except ImportError:
    sys.modules['fcntl'] = types.SimpleNamespace(LOCK_EX=2, flock=lambda *_: None)

REPO = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('sg_freeze_safety', REPO / 'ops/sg_migration_20261105_freeze.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class FreezeSafety(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=REPO / 'runtime', prefix='sg-freeze-unit-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.calls = []

    def test_target_write_fence_always_wins(self):
        (self.root / 'target-write-commit.json').write_text('{}')
        (self.root / 'rollback.json').write_text('{"rolled_back":true}')
        with patch.object(m, 'run', side_effect=AssertionError('no subprocess allowed')):
            with self.assertRaisesRegex(RuntimeError, 'stale-source rollback forbidden'):
                m.restore(self.root)

    def test_completed_rollback_is_noop(self):
        (self.root / 'rollback.json').write_text('{"rolled_back":true}')
        with patch.object(m, 'run', side_effect=AssertionError('no duplicate restart')):
            self.assertTrue(m.restore(self.root)['already_restored'])

    def test_unknown_edge_config_never_overwritten(self):
        conf = self.root / 'nginx.conf'
        conf.write_bytes(b'unrelated live configuration')
        (self.root / 'freeze-state.private.json').write_text(json.dumps({
            'original_config_sha256': 'before', 'gated_config_sha256': 'gate'}))
        with patch.object(m, 'CONFIG', conf), patch.object(m, 'run', side_effect=AssertionError('no command')):
            with self.assertRaisesRegex(RuntimeError, 'source edge changed'):
                m.restore(self.root)
        self.assertEqual(conf.read_bytes(), b'unrelated live configuration')

    def test_dynamic_skip_is_narrow(self):
        self.assertTrue(m.deferred_static_path(m.DEPLOY / 'production/runtime/payment.json'))
        sync = Path('/srv/omni-data/model-sync-20261105')
        for name in ['last-run.json', 'preview-123.private.json', 'before-123.private.json']:
            self.assertTrue(m.deferred_static_path(sync / name))
        for name in ['config.json', 'manifest.json', 'model_sync_worker_20261105.py', 'preview-code.py']:
            self.assertFalse(m.deferred_static_path(sync / name))
        self.assertFalse(m.deferred_static_path(m.DEPLOY / 'production/xorpay-control/policy.json'))

    def test_commit_never_marks_when_source_writer_is_running(self):
        (self.root / 'frozen.json').write_text('{}')
        (self.root / 'freeze-state.private.json').write_text(json.dumps({'deadline': 2000, 'units': []}))
        with patch.object(m.time, 'time', return_value=1000), \
             patch.object(m, 'run', return_value='[{"State":{"Running":true}}]'), \
             patch.object(m, 'private_json', side_effect=AssertionError('must not mark target handoff')):
            with self.assertRaisesRegex(AssertionError, 'source writer'):
                m.commit(self.root)
        self.assertFalse((self.root / 'target-write-commit.json').exists())

    def test_commit_marks_before_cancelling_watchdog(self):
        (self.root / 'frozen.json').write_text('{}')
        (self.root / 'freeze-state.private.json').write_text(json.dumps({'deadline': 2000, 'units': []}))
        events = []
        def mark(path, value):
            path.write_text(json.dumps(value));events.append('marker')
        with patch.object(m.time, 'time', return_value=1000), \
             patch.object(m, 'run', return_value='[{"State":{"Running":false}}]'), \
             patch.object(m, 'private_json', side_effect=mark), \
             patch.object(m.subprocess, 'run', side_effect=lambda *a, **k: events.append('cancel_timer')):
            self.assertTrue(m.commit(self.root)['committed'])
        self.assertEqual(events, ['marker', 'cancel_timer'])
        with patch.object(m, 'run', side_effect=AssertionError('no stale-source access')):
            self.assertTrue(m.commit(self.root)['already_committed'])

    def test_timeout_rolls_back_without_stopping_customer_stream(self):
        deploy = self.root / 'deploy'
        (deploy / 'nginx-ha').mkdir(parents=True)
        conf = deploy / 'nginx-ha/https.conf'
        conf.write_bytes(b'\n'.join([b' server_name omniapi.biz;', b' server_name www.omniapi.biz;',
                                    b' server_name omniapi.hyeroomy.com;', b' server_name _;']))
        compose = deploy / 'compose.primary.json'
        compose.write_text('{}')
        binary = self.root / 'new-api'
        binary.write_bytes(b'fixed executable')
        h = m.digest(binary)
        state = {'compose_sha256': m.digest(compose), 'binary': str(binary), 'binary_sha256': h,
                 'source_units': [{'name': 'omni-ha-guard.service', 'active': True}]}
        (self.root / 'source-state.private.json').write_text(json.dumps(state))
        (self.root / 'static-files.json').write_text('{}')
        def fake_run(args, *_, **__):
            self.calls.append(args)
            if args[:3] == ['docker', 'exec', m.APP] and 'sha256sum' in args:
                return h + ' /new-api'
            if args[:2] == ['docker', 'inspect']:
                return json.dumps([{'Name': '/' + m.APP, 'State': {'Running': True}}])
            if args[:2] == ['systemctl', 'show']:
                return 'active'
            if args[0] == 'curl':
                return '503'
            return ''
        def fake_sql(q):
            return 'f' if 'pg_is_in_recovery' in q else ('on' if 'synchronous_commit' in q else '')
        def private(path, data):
            path.write_text(json.dumps(data))
        fake_cfg = self.root / 'monitor.json'
        fake_cfg.write_text('{"gateway_origin":"http://loopback","gateway_token":"fixture"}')
        original_read = Path.read_text
        def read(path, *a, **kw):
            if str(path).replace('\\', '/').endswith('/opt/omni-node-monitor/config.json'):
                return original_read(fake_cfg)
            return original_read(path, *a, **kw)
        response = types.SimpleNamespace(status=200)
        response.__enter__ = lambda *_: response
        class Reply:
            def __enter__(self): return self
            def __exit__(self, *_): pass
            def read(self, *_): return b'{"data":{"active":1,"model_active":1}}'
        with patch.object(m, 'DEPLOY', deploy), patch.object(m, 'CONFIG', conf), \
             patch.object(m, 'run', side_effect=fake_run), patch.object(m, 'sql', side_effect=fake_sql), \
             patch.object(m, 'private_json', side_effect=private), \
             patch.object(m.os, 'geteuid', return_value=0, create=True), \
             patch.object(Path, 'read_text', read), \
             patch('urllib.request.urlopen', return_value=Reply()), \
             patch.object(m.time, 'monotonic', side_effect=[0, 301]), \
             patch.object(m.time, 'sleep'), patch.object(m.signal, 'signal'), \
             patch.object(m, 'restore', return_value={'rolled_back': True}) as restore:
            with self.assertRaisesRegex(AssertionError, 'drain deadline'):
                m.freeze(self.root)
            restore.assert_called_once_with(self.root)
        self.assertFalse(any(args[:2] == ['docker', 'stop'] for args in self.calls))
        timer = next(args for args in self.calls if args[0] == 'systemd-run')
        self.assertIn('--on-active=480s', timer)


if __name__ == '__main__':
    unittest.main()
