import tempfile,unittest,hashlib
from pathlib import Path
from scripts.commerce_release_preflight import table_approval_matches

class QuoteApprovalTest(unittest.TestCase):
    def test_approval_does_not_survive_a_changed_allowance_table(self):
        parent=Path(__file__).resolve().parents[1]/'runtime/commerce-provenance-tests';parent.mkdir(parents=True,exist_ok=True)
        with tempfile.TemporaryDirectory(dir=parent) as d:
            p=Path(d)/'quote.json';p.write_text('{"allowance":100}')
            approval={'approved':True,'quote_sha256':hashlib.sha256(p.read_bytes()).hexdigest()}
            self.assertTrue(table_approval_matches(p,approval))
            p.write_text('{"allowance":200}');self.assertFalse(table_approval_matches(p,approval))
            self.assertFalse(table_approval_matches(p,{'approved':True,'quote_sha256':'placeholder'}))
            p.unlink();self.assertFalse(table_approval_matches(p,approval))

if __name__=='__main__':unittest.main()
