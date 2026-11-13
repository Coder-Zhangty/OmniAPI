import importlib.util
import tempfile
import unittest
import zipfile
from pathlib import Path

spec = importlib.util.spec_from_file_location('client_release', Path(__file__).resolve().parents[1] / 'ops/clients/release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseArtifactTests(unittest.TestCase):
    def test_development_apk_cannot_be_a_standalone_release(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'synthetic.apk'
            with zipfile.ZipFile(path, 'w') as archive:
                archive.writestr('AndroidManifest.xml', b'synthetic manifest')
                archive.writestr('lib/arm64-v8a/synthetic.so', bytes(1024 * 1024))
            with self.assertRaisesRegex(ValueError, 'Metro'):
                release.validate_artifact(path, 'android-arm64')
            with zipfile.ZipFile(path, 'a') as archive:
                archive.writestr('assets/index.android.bundle', b'synthetic bundle')
            digest, size = release.validate_artifact(path, 'android-arm64')
            self.assertEqual(len(digest), 64)
            self.assertGreater(size, 1024 * 1024)

    def test_corresponding_sources_need_both_licenses_and_exclude_signing_keys(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'synthetic-source.zip'
            with zipfile.ZipFile(path, 'w') as archive:
                for platform in ['desktop', 'android']:
                    for name in ['LICENSE', 'OMNIAPI-UPSTREAM.json', 'OMNIAPI-SOURCE.md']:
                        archive.writestr(f'{platform}/{name}', 'synthetic')
                archive.writestr('desktop/electron-builder.yml', 'appId: biz.omniapi.desktop\nnsis:\n  guid: a5e8d5f7-24d3-5d74-bea8-4d63ff577453\n')
            release.validate_source(path)
            with zipfile.ZipFile(path, 'a') as archive:
                archive.writestr('android/private.jks', b'synthetic')
            with self.assertRaisesRegex(ValueError, 'Private material'):
                release.validate_source(path)

    def test_upstream_windows_identity_is_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'synthetic-source.zip'
            with zipfile.ZipFile(path, 'w') as archive:
                for platform in ['desktop', 'android']:
                    for name in ['LICENSE', 'OMNIAPI-UPSTREAM.json', 'OMNIAPI-SOURCE.md']:
                        archive.writestr(f'{platform}/{name}', 'synthetic')
                archive.writestr('desktop/electron-builder.yml', 'appId: biz.omniapi.desktop\nnsis:\n  guid: 41a4ccd8-bcc0-5710-9eee-0e164da68057\n')
            with self.assertRaisesRegex(ValueError, 'identity'):
                release.validate_source(path)


if __name__ == '__main__':
    unittest.main()
