"""Local-only chain, exact-base, frozen-artifact and SQLite-WAL checks."""
import base64,hashlib,importlib.util,io,json,pathlib,sqlite3,tarfile,tempfile,unittest
from unittest.mock import patch
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from test_sg_migration_backup_20261105 import BackupProof,m as backup,ROOT

spec=importlib.util.spec_from_file_location('delta_test',ROOT/'ops/sg_migration_20261105_delta.py');delta=importlib.util.module_from_spec(spec);spec.loader.exec_module(delta)

def tar_bytes(files):
 out=io.BytesIO()
 with tarfile.open(fileobj=out,mode='w:gz') as archive:
  for name,data in files.items():
   entry=tarfile.TarInfo(name);entry.size=len(data);entry.mode=0o600;archive.addfile(entry,io.BytesIO(data))
 return out.getvalue()

class DeltaProof(unittest.TestCase):
 def setUp(self):
  self.fixture=BackupProof();self.fixture.setUp();self.addCleanup(self.fixture.doCleanups);self.root=self.fixture.root
  self.codec=backup.codec(ROOT/'ops/migration/business_backup.py');key=rsa.generate_private_key(public_exponent=65537,key_size=2048)
  self.private=self.root/'private.pem';self.private.write_bytes(key.private_bytes(serialization.Encoding.PEM,serialization.PrivateFormat.PKCS8,serialization.NoEncryption()))
  self.public=key.public_key().public_bytes(serialization.Encoding.PEM,serialization.PublicFormat.SubjectPublicKeyInfo)
  self.base=self.root/'base.ohab';self.base.write_bytes(self.codec.encrypt(self.fixture.payload(),self.public))
 def session(self,path):
  envelope=backup.read_envelope_header(path);return backup.unwrap_envelope_key(envelope,self.private),backup.digest(envelope)
 def chain(self,bad_artifact=False):
  state=self.root/'state.sqlite3'
  with sqlite3.connect(state) as db:db.execute('CREATE TABLE fixture(v)');db.execute('INSERT INTO fixture VALUES (42)');db.commit()
  data=state.read_bytes();final_files={'state.sqlite3':{'bytes':len(data),'sha256':backup.digest(data)}}
  artifacts={'final.dump':b'PGDMP synthetic fixture','roles.sql':b'roles fixture','baseline.json':b'{"tables":{"latest":{}},"sequences":{}}','financial-summary.json':b'{}','redis.tar.gz':tar_bytes({'redis/dump.rdb':b'REDIS fixture'}),'final.tar.gz':tar_bytes({'state.sqlite3':data}),'final-files.json':json.dumps(final_files).encode(),'redis-keyspace.json':b'{}'}
  frozen={'frozen':True,'at':1234,'binary_sha256':backup.digest(b'executable fixture'),'artifacts':{n:{'bytes':len(raw),'sha256':backup.digest(raw)} for n,raw in artifacts.items()}}
  if bad_artifact:frozen['artifacts']['roles.sql']['sha256']='0'*64
  refresh=tar_bytes({});state={'binary_sha256':frozen['binary_sha256'],'static_refresh_sha256':backup.digest(refresh),'static_refresh_paths':[],'images_sha256':backup.file_digest(self.fixture.images),'images':{'runtime':'sha256:fixture'}}
  files={'artifacts/'+n:v for n,v in artifacts.items()};files.update({'receipts/frozen.json':json.dumps(frozen).encode(),'receipts/source-state.private.json':json.dumps(state).encode(),'receipts/static-files.json':b'{}','receipts/static-refresh.tar.gz':refresh})
  files['overlay/'+str(delta.DEPLOY/'compose.primary.json').lstrip('/')]=b'{"services":{"api":{"image":"sha256:fixture"}}}'
  chain={'format':delta.FORMAT,'standalone':False,'frozen_at':1234,'base':{'name':self.base.name,'sha256':backup.file_digest(self.base),'bytes':self.base.stat().st_size},'files':{n:{'bytes':len(raw),'sha256':backup.digest(raw),'mode':0o600,'uid':0,'gid':0} for n,raw in files.items()}}
  files['chain.json']=json.dumps(chain).encode();path=self.root/'delta.ohab';path.write_bytes(self.codec.encrypt(tar_bytes(files),self.public));return path
 def verify(self,path,postgres=False,at=1234):
  bk,bh=self.session(self.base);dk,dh=self.session(path)
  with patch.object(delta,'load_backup',return_value=backup):return delta.verify_chain(path,self.base,bk,dk,bh,dh,self.fixture.images,self.root/'freeze.py',postgres,at)
 def test_complete_chain_validates_exact_base_and_latest_sqlite(self):
  proof=self.verify(self.chain());self.assertTrue(proof['chain_verified']);self.assertFalse(proof['standalone']);self.assertEqual(proof['sqlite_integrity_verified'],1)
 def test_frozen_artifact_checksum_is_independently_required(self):
  with self.assertRaisesRegex(AssertionError,'frozen artifact checksum mismatch'):self.verify(self.chain(True))
 def test_stale_round_rejected(self):
  with self.assertRaisesRegex(AssertionError,'stale freeze receipt'):self.verify(self.chain(),at=999)
 def test_missing_base_rejected(self):
  path=self.chain();self.base.write_bytes(b'corrupted full base')
  with self.assertRaises(Exception):self.verify(path)
 def test_latest_postgres_restore_uses_latest_encrypted_baseline(self):
  called=[]
  def verify(data,baseline,helper):
   called.append(json.loads(baseline.read_text()));return {'postgres_restore_verified':True,'postgres_tables_verified':1,'postgres_sequences_verified':0}
  with patch.object(backup,'postgres_restore_check',side_effect=verify):proof=self.verify(self.chain(),True)
  self.assertEqual(called,[{'tables':{'latest':{}},'sequences':{}}]);self.assertTrue(proof['postgres_restore_verified'])
 def test_final_member_hash_and_membership_are_exact(self):
  raw=tar_bytes({'state.json':b'{}'})
  with self.assertRaisesRegex(AssertionError,'membership mismatch'):delta.verify_final_archive(raw,{},backup)
  with self.assertRaisesRegex(AssertionError,'checksum mismatch'):delta.verify_final_archive(raw,{'/state.json':{'bytes':2,'sha256':'0'*64}},backup)
 def test_final_sqlite_wal_is_recovered_before_integrity_check(self):
  path=self.root/'wal.sqlite3';db=sqlite3.connect(path);self.addCleanup(db.close);db.execute('PRAGMA journal_mode=WAL');db.execute('CREATE TABLE fixture(v)');db.execute('INSERT INTO fixture VALUES (7)');db.commit()
  files={'wal.sqlite3':path.read_bytes(),'wal.sqlite3-wal':path.with_name(path.name+'-wal').read_bytes()};expected={n:{'bytes':len(raw),'sha256':backup.digest(raw)} for n,raw in files.items()}
  proof=delta.verify_final_archive(tar_bytes(files),expected,backup);self.assertEqual(proof['sqlite_integrity_verified'],1)

if __name__=='__main__':unittest.main()
