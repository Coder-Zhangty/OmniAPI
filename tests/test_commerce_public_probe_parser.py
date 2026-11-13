"""Exercise the actual probe parser with synthetic SSE, without network or keys."""
import ast,base64,json,struct,time,unittest,urllib.request,zlib
from pathlib import Path

class Response:
    def __init__(self,events):self.lines=[b'data: '+(e.encode() if isinstance(e,str) else json.dumps(e).encode())+b'\n\n' for e in events]
    def __enter__(self):return self
    def __exit__(self,*_):pass
    def __iter__(self):return iter(self.lines)

class Opener:
    def __init__(self,events):self.events=events
    def open(self,request,timeout):return Response(self.events)

def parser(events):
    source=Path(__file__).resolve().parents[1]/'ops/commerce_public_capabilities_20261104.py'
    tree=ast.parse(source.read_text(encoding='utf-8'))
    selected=[n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name in ('post','fixture_png')]
    scope={'time':time,'json':json,'urllib':urllib,'opener':Opener(events),'headers':{},'base64':base64,'struct':struct,'zlib':zlib}
    exec(compile(ast.Module(body=selected,type_ignores=[]),str(source),'exec'),scope)
    return scope

class PublicProbeParserTest(unittest.TestCase):
    def test_openai_stream_assembles_function_argument_fragments(self):
        values=[{'choices':[{'delta':{'tool_calls':[{'index':0,'id':'synthetic-call','function':{'name':'ping','arguments':'{"message":'}}]}}]},
                {'choices':[{'delta':{'tool_calls':[{'index':0,'function':{'arguments':'"OK"}'}}]}}]},'[DONE]']
        out=parser(values)['post']('/v1/chat/completions',{'model':'synthetic'})
        self.assertTrue(out['terminal']);call=out['tool_calls'][0]
        self.assertEqual(call['function']['name'],'ping');self.assertEqual(json.loads(call['function']['arguments']),{'message':'OK'})
    def test_native_stream_preserves_tool_id_and_parses_result_input(self):
        values=[{'type':'content_block_start','index':0,'content_block':{'type':'tool_use','id':'synthetic-tool','name':'ping','input':{}}},
                {'type':'content_block_delta','index':0,'delta':{'type':'input_json_delta','partial_json':'{"message":"OK"}'}},
                {'type':'message_stop'}]
        out=parser(values)['post']('/v1/messages',{'model':'synthetic'})
        self.assertTrue(out['terminal']);self.assertEqual(out['content'][0]['id'],'synthetic-tool');self.assertEqual(out['content'][0]['input'],{'message':'OK'})
    def test_http200_without_terminal_event_is_not_a_pass(self):
        out=parser([{'choices':[{'delta':{'content':'OK'}}]}])['post']('/v1/chat/completions',{})
        self.assertEqual(out['text'],'OK');self.assertFalse(out['terminal'])
    def test_sse_error_is_not_accepted_as_a_success(self):
        with self.assertRaises(AssertionError):parser([{'error':{'code':'synthetic-error'}}])['post']('/v1/messages',{})
    def test_visual_fixture_dimensions_are_explicit_and_small(self):
        png=base64.b64decode(parser([])['fixture_png']());self.assertEqual(png[:8],b'\x89PNG\r\n\x1a\n');self.assertEqual(struct.unpack('>II',png[16:24]),(96,32));self.assertLess(len(png),1024)

if __name__=='__main__':unittest.main()
