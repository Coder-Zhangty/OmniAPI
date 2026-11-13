"""Docker image identity differs between classic storage and OCI/containerd storage."""
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
from archive_restore_gateway import verify_image_archive


class ImageFormatTests(unittest.TestCase):
    def package(self, path, entries):
        with tarfile.open(path, 'w:gz') as archive:
            for name, data in entries.items():
                member = tarfile.TarInfo(name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))

    def test_classic_config_digest(self):
        config = b'{"architecture":"amd64"}'
        digest = 'sha256:' + hashlib.sha256(config).hexdigest()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'image.gz'
            self.package(path, {'config.json': config, 'manifest.json': b'[{"Config":"config.json"}]'})
            verify_image_archive(path, digest)
            with self.assertRaises(ValueError):
                verify_image_archive(path, 'sha256:' + '0' * 64)

    def test_oci_index_digest_and_tampered_layer(self):
        entries = {}
        def blob(data, media):
            digest = 'sha256:' + hashlib.sha256(data).hexdigest()
            entries['blobs/sha256/' + digest[7:]] = data
            return {'digest': digest, 'size': len(data), 'mediaType': media}
        layer = blob(b'synthetic-layer', 'application/vnd.oci.image.layer.v1.tar')
        config = blob(b'{}', 'application/vnd.oci.image.config.v1+json')
        manifest = blob(json.dumps({'config': config, 'layers': [layer]}).encode(), 'application/vnd.oci.image.manifest.v1+json')
        index = blob(json.dumps({'manifests': [manifest]}).encode(), 'application/vnd.oci.image.index.v1+json')
        entries['index.json'] = json.dumps({'manifests': [index]}).encode()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'image.gz'
            self.package(path, entries)
            verify_image_archive(path, index['digest'])
            entries['blobs/sha256/' + layer['digest'][7:]] = b'tampered-layer'
            self.package(path, entries)
            with self.assertRaises(ValueError):
                verify_image_archive(path, index['digest'])


if __name__ == '__main__':
    unittest.main()
