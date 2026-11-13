"""Pure archive/receipt tests; no Docker, SSH, cloud APIs or production paths."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import tarfile
import unittest
from unittest.mock import patch

REPO=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('sg_prepare_source',REPO/'ops/sg_migration_20261105_prepare_source.py')
module=importlib.util.module_from_spec(spec)
# Import must not create ROOT, inspect Docker or query systemd.
with patch('subprocess.run',side_effect=AssertionError('import must be inert')):
 spec.loader.exec_module(module)


def add(archive,name,data):
 info=tarfile.TarInfo(name);info.size=len(data);archive.addfile(info,io.BytesIO(data))


class PrepareSource(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory(dir=REPO/'runtime',prefix='sg-precopy-unit-')
  self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)

 def image_archive(self,config=b'{"architecture":"amd64"}',missing_layer=False,wrong_config=False):
  digest=hashlib.sha256(config).hexdigest();path=self.root/'images.tar.gz'
  with tarfile.open(path,'w:gz') as tar:
   add(tar,digest+'.json',b'tampered' if wrong_config else config)
   if not missing_layer:add(tar,'layers/first/layer.tar',b'synthetic layer')
   add(tar,'manifest.json',json.dumps([{'Config':digest+'.json','Layers':['layers/first/layer.tar'],'RepoTags':None}]).encode())
  return path,'sha256:'+digest

 def test_static_manifest_hashes_archived_bytes_and_symlink(self):
  path=self.root/'static.tar.gz';data=b'exact archived immutable source'
  with tarfile.open(path,'w:gz') as tar:
   add(tar,'srv/business/code.py',data)
   info=tarfile.TarInfo('srv/business/current');info.type=tarfile.SYMTYPE;info.linkname='code.py';tar.addfile(info)
  result=module.static_manifest(path)
  self.assertEqual(result,{'srv/business/code.py':{'bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()},'srv/business/current':{'symlink':'code.py'}})

 def test_static_conflicting_duplicate_refused(self):
  path=self.root/'static.tar.gz'
  with tarfile.open(path,'w:gz') as tar:add(tar,'srv/code.py',b'first');add(tar,'srv/code.py',b'changed')
  with self.assertRaisesRegex(AssertionError,'conflicting'):module.static_manifest(path)

 def test_manifest_proves_exact_image_id(self):
  path,image= self.image_archive()
  self.assertEqual(module.archive_image_ids(path),{image})

 def test_containerd_blob_config_manifest_supported(self):
  path=self.root/'oci-images.tar.gz';config=b'{"os":"linux","architecture":"amd64"}'
  digest=hashlib.sha256(config).hexdigest();name='blobs/sha256/'+digest
  with tarfile.open(path,'w:gz') as tar:
   add(tar,name,config);add(tar,'blobs/sha256/layer',b'fixture')
   add(tar,'manifest.json',json.dumps([{'Config':name,'Layers':['blobs/sha256/layer']}]).encode())
  self.assertEqual(module.archive_image_ids(path),{'sha256:'+digest})

 def test_missing_layer_and_forged_config_fail(self):
  path,_=self.image_archive(missing_layer=True)
  with self.assertRaisesRegex(AssertionError,'layer missing'):module.archive_image_ids(path)
  path,_=self.image_archive(wrong_config=True)
  with self.assertRaisesRegex(AssertionError,'digest mismatch'):module.archive_image_ids(path)

 def test_unknown_existing_cache_never_reused(self):
  path,image=self.image_archive()
  with patch.object(module.subprocess,'Popen',side_effect=AssertionError('no export or process')):
   with self.assertRaisesRegex(AssertionError,'receipt missing'):
    module.export_or_reuse_images(path,{'live:tag':image})

 def test_receipt_hash_and_image_mapping_both_required(self):
  path,image=self.image_archive();images={'live:tag':image};receipt={'images':images,'images_sha256':module.sha(path)}
  proof=module.validate_image_cache(path,images,receipt)
  self.assertTrue(proof['manifest_image_ids_verified'])
  with self.assertRaisesRegex(AssertionError,'mapping changed'):
   module.validate_image_cache(path,{'other:tag':image},receipt)
  receipt['images_sha256']='0'*64
  with self.assertRaisesRegex(AssertionError,'hash mismatch'):
   module.validate_image_cache(path,images,receipt)

 def test_archive_content_must_match_live_ids_even_with_matching_receipt_hash(self):
  path,_=self.image_archive();images={'live:tag':'sha256:'+'1'*64}
  with self.assertRaisesRegex(AssertionError,'exact live image IDs'):
   module.validate_image_cache(path,images,{'images':images,'sha256':module.sha(path)})


if __name__=='__main__':unittest.main()
