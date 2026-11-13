"""Linux worker regression: systemd's private umask must not hide public files."""
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[1]
CODE=Path(os.environ.get('OMNI_RANKINGS_TEST_CODE',ROOT/'ops/rankings'))
CONFIG=Path(os.environ.get('OMNI_RANKINGS_TEST_CONFIG',ROOT/'config/rankings-display.json'))


@unittest.skipUnless(os.name=='posix','worker runs on Linux production hosts')
class WorkerTests(unittest.TestCase):
    def setUp(self):
        sys.path.insert(0,str(CODE))
        import snapshot
        self.s=snapshot
        self.temp=tempfile.TemporaryDirectory()
        self.root=Path(self.temp.name)
        self.state=self.root/'state';self.state.mkdir(mode=0o700)
        self.public=self.root/'rankings';self.public.mkdir(mode=0o755)
        self.raw={'recovery':True,'lag_seconds':1,'as_of':snapshot.midnight('2026-10-16'),
                  'rows':[[snapshot.midnight('2026-09-26')+d*86400,'gpt-test',101+d] for d in range(20)]}
        self.input=self.root/'input.json'

    def tearDown(self):self.temp.cleanup()

    def invoke(self):
        self.input.write_text(json.dumps(self.raw))
        return subprocess.run([sys.executable,str(CODE/'worker.py'),'--state',str(self.state),'--public',str(self.public),
                               '--config',str(CONFIG),'--input',str(self.input),'--init-history'],
                              capture_output=True,text=True,timeout=15,preexec_fn=lambda:os.umask(0o077))

    def assert_public(self):
        manifest=json.loads((self.public/'manifest.json').read_text())
        for entry in manifest['periods'].values():
            file=self.public/entry['path']
            self.assertEqual(stat.S_IMODE(file.stat().st_mode),0o644)
            self.assertEqual(stat.S_IMODE(file.parent.stat().st_mode),0o755)
        self.assertEqual(stat.S_IMODE((self.public/'versions').stat().st_mode),0o755)
        self.assertEqual(stat.S_IMODE((self.state/'history.json').stat().st_mode),0o600)

    def test_umask_refresh_freeze_and_failure_preserves_manifest(self):
        first=self.invoke();self.assertEqual(first.returncode,0,first.stderr)
        self.assert_public()
        frozen=(self.state/'history.json').read_bytes()
        old=(self.public/'manifest.json').read_bytes()
        self.raw['as_of']+=300
        self.raw['rows'].append([self.raw['as_of'],'gpt-test',137])
        second=self.invoke();self.assertEqual(second.returncode,0,second.stderr)
        self.assert_public()
        self.assertEqual((self.state/'history.json').read_bytes(),frozen)
        current=(self.public/'manifest.json').read_bytes()
        self.assertNotEqual(current,old)
        self.assertEqual((self.state/'manifest.previous.json').read_bytes(),old)
        self.raw['recovery']=False
        failed=self.invoke();self.assertNotEqual(failed.returncode,0)
        self.assertEqual((self.public/'manifest.json').read_bytes(),current)

    def test_concurrent_worker_skips_without_publishing(self):
        import fcntl
        with (self.state/'worker.lock').open('a') as lock:
            fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
            result=self.invoke()
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(json.loads(result.stdout),{'skipped':'already running'})
            self.assertFalse((self.public/'manifest.json').exists())


if __name__=='__main__':unittest.main(verbosity=2)
