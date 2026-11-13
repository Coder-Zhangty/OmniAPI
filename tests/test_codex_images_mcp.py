import base64,importlib.util,json,os,pathlib,struct,subprocess,sys,tempfile,threading,unittest,zlib
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from unittest.mock import patch
ROOT=pathlib.Path(__file__).resolve().parents[1];SCRIPT=ROOT/'clients/codex-images/omni_images_mcp.py'
spec=importlib.util.spec_from_file_location('omni_images',SCRIPT);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
def png(size=1024):
 def chunk(kind,value):return struct.pack('>I',len(value))+kind+value+struct.pack('>I',zlib.crc32(kind+value)&0xffffffff)
 return b'\x89PNG\r\n\x1a\n'+chunk(b'IHDR',struct.pack('>IIBBBBB',size,size,8,2,0,0,0))+chunk(b'IDAT',zlib.compress((b'\x00'+b'\xff'*(size*3))*size))+chunk(b'IEND',b'')
PNG=png()
class Handler(BaseHTTPRequestHandler):
 calls=[];status=200;result={'data':[{'b64_json':base64.b64encode(PNG).decode()}]}
 def log_message(self,*a):pass
 def do_GET(self):
  data=json.dumps({'data':{'models':{'gpt-image-2':{'image_specs':{'size':'1024x1024','qualities':['low','medium','high']}},'gpt-image-2.5':{'image_specs':{'sizes':['1024x1024','4096x4096'],'qualities':['low','high'],'options':[{'size':'1024x1024','quality':'low'},{'size':'4096x4096','quality':'high'}]}}}}}).encode()
  self.send_response(200);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
 def do_POST(self):
  body=json.loads(self.rfile.read(int(self.headers['Content-Length'])));self.__class__.calls.append((self.path,self.headers.get('Authorization'),body))
  data=json.dumps(self.__class__.result).encode();self.send_response(self.__class__.status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(data)));self.send_header('X-Oneapi-Request-Id','synthetic-image-request');self.end_headers();self.wfile.write(data)
class ImagesMCP(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  cls.server=ThreadingHTTPServer(('127.0.0.1',0),Handler);cls.thread=threading.Thread(target=cls.server.serve_forever,daemon=True);cls.thread.start()
 @classmethod
 def tearDownClass(cls):cls.server.shutdown();cls.server.server_close();cls.thread.join()
 def setUp(self):
  tmp=ROOT/'runtime/image-generation-release-20261015/test-temp';tmp.mkdir(parents=True,exist_ok=True)
  self.temp=tempfile.TemporaryDirectory(dir=tmp);self.addCleanup(self.temp.cleanup);Handler.calls=[];Handler.status=200;Handler.result={'data':[{'b64_json':base64.b64encode(PNG).decode()}]}
  self.environment=patch.dict(os.environ,{'OMNI_BASE_URL':'http://127.0.0.1:'+str(self.server.server_port)+'/v1','OMNI_API_KEY':'synthetic-platform-key','OMNI_IMAGE_OUTPUT_DIR':self.temp.name});self.environment.start();self.addCleanup(self.environment.stop)
 def test_real_stdio_tool_call_saves_png_and_correlates_request(self):
  messages=[{'jsonrpc':'2.0','id':1,'method':'initialize','params':{'protocolVersion':'2025-06-18'}},{'jsonrpc':'2.0','method':'notifications/initialized'},{'jsonrpc':'2.0','id':2,'method':'tools/list'},{'jsonrpc':'2.0','id':3,'method':'tools/call','params':{'name':'omni_generate_image','arguments':{'prompt':'A blue circle','filename':'circle.png'}}}]
  p=subprocess.run([sys.executable,str(SCRIPT)],input='\n'.join(map(json.dumps,messages))+'\n',capture_output=True,text=True,timeout=10);self.assertEqual(p.returncode,0);self.assertFalse(p.stderr);rows=list(map(json.loads,p.stdout.splitlines()));self.assertEqual(len(rows),3);self.assertEqual(rows[1]['result']['tools'][0]['name'],'omni_generate_image');self.assertFalse(rows[2]['result']['isError']);result=json.loads(rows[2]['result']['content'][0]['text']);self.assertEqual(result['request_id'],'synthetic-image-request');self.assertEqual(pathlib.Path(result['path']).read_bytes(),PNG);self.assertEqual(Handler.calls[0][0],'/v1/images/generations');self.assertEqual(Handler.calls[0][1],'Bearer synthetic-platform-key');self.assertEqual(Handler.calls[0][2]['model'],'gpt-image-2');self.assertEqual(Handler.calls[0][2]['n'],1);self.assertNotIn('synthetic-platform-key',p.stdout)
 def test_gateway_error_never_creates_image_or_retries_and_redacts_key(self):
  Handler.status=503;Handler.result={'error':{'message':'Synthetic failure synthetic-platform-key'}}
  result=m.handle({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'omni_generate_image','arguments':{'prompt':'A circle'}}})['result'];self.assertTrue(result['isError']);self.assertEqual(len(Handler.calls),1);self.assertFalse(list(pathlib.Path(self.temp.name).iterdir()));self.assertNotIn('synthetic-platform-key',result['content'][0]['text']);self.assertIn('synthetic-image-request',result['content'][0]['text'])
 def test_quality_reaches_upstream_without_silent_downgrade(self):
  for quality in ['low','medium','high']:
   result=m.generate({'prompt':'A teapot','quality':quality,'filename':quality+'.png'})
   self.assertEqual(Handler.calls[-1][2]['quality'],quality);self.assertEqual(result['quality'],quality)
  result=m.generate({'prompt':'A teapot','filename':'default.png'});self.assertEqual(result['quality'],'low')
  count=len(Handler.calls)
  for quality in ['auto','ultra',None,[],{}]:
   with self.assertRaises(m.ImageError):m.generate({'prompt':'A teapot','quality':quality})
  self.assertEqual(len(Handler.calls),count)
 def test_invalid_png_is_not_saved(self):
  Handler.result={'data':[{'b64_json':base64.b64encode(PNG[:-5]).decode()}]}
  with self.assertRaises(m.ImageError):m.generate({'prompt':'A circle'})
  self.assertFalse(list(pathlib.Path(self.temp.name).iterdir()))
 def test_current_supplier_default_and_explicit_legacy_size_are_not_confused(self):
  image=png(1254);Handler.result={'data':[{'b64_json':base64.b64encode(image).decode()}]}
  catalog={'gpt-image-2':{'image_specs':{'size':'1254x1254','sizes':['1254x1254'],'qualities':['low','medium','high'],'default_quality':'low'}}}
  with patch.object(m,'image_catalog',return_value=catalog):
   result=m.generate({'prompt':'Synthetic native size','filename':'native.png'})
   self.assertEqual(result['width'],1254);self.assertEqual(Handler.calls[-1][2]['size'],'1254x1254')
   count=len(Handler.calls)
   with self.assertRaises(m.ImageError):m.generate({'prompt':'Synthetic explicit legacy size','size':'1024x1024'})
   self.assertEqual(len(Handler.calls),count)
 def test_model_and_dimensions_are_never_silently_substituted(self):
  with self.assertRaises(m.ImageError):m.generate({'prompt':'A circle','model':'gpt-image-2.5','size':'4096x4096','quality':'high'})
  self.assertEqual(Handler.calls[-1][2]['model'],'gpt-image-2.5');self.assertEqual(Handler.calls[-1][2]['size'],'4096x4096')
  self.assertFalse(list(pathlib.Path(self.temp.name).iterdir()))
  count=len(Handler.calls)
  for args in [{'model':'unverified-image'},{'model':'gpt-image-2.5','size':'4096x4096','quality':'low'},{'service_tier':'priority'}]:
   with self.assertRaises(m.ImageError):m.generate({'prompt':'A circle',**args})
  self.assertEqual(len(Handler.calls),count)
 def test_existing_file_and_traversal_rejected_before_spending(self):
  path=pathlib.Path(self.temp.name)/'keep.png';path.write_bytes(b'existing')
  for name in ['../outside.png','C:/outside.png','CON.png','keep.png']:
   with self.assertRaises(m.ImageError):m.generate({'prompt':'A circle','filename':name})
  self.assertEqual(path.read_bytes(),b'existing');self.assertFalse(Handler.calls)
 def test_http_redirect_never_forwards_credential(self):
  class Redirect(Handler):
   def do_POST(self):self.send_response(302);self.send_header('Location','http://127.0.0.1:'+str(self.server.server_port)+'/unexpected');self.send_header('Content-Length','0');self.end_headers()
  server=ThreadingHTTPServer(('127.0.0.1',0),Redirect);thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
  try:
   with patch.dict(os.environ,{'OMNI_BASE_URL':'http://127.0.0.1:'+str(server.server_port)+'/v1'}):
    with self.assertRaises(m.ImageError):m.generate({'prompt':'A circle'})
   self.assertFalse(Handler.calls)
  finally:server.shutdown();server.server_close();thread.join()
if __name__=='__main__':unittest.main()
