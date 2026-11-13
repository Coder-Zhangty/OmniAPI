import tempfile,unittest
from pathlib import Path
from ops.commerce_artifact_provenance import source_identity,require_matching_acceptance

class AcceptanceProvenanceTest(unittest.TestCase):
    def temporary_workspace(self):
        parent=Path(__file__).resolve().parents[1]/'runtime/commerce-provenance-tests'
        parent.mkdir(parents=True,exist_ok=True)
        return tempfile.TemporaryDirectory(dir=parent)
    def test_scope_change_invalidates_previous_database_acceptance(self):
        with self.temporary_workspace() as d:
            root=Path(d);(root/'model').mkdir();scope=root/'model/commerce-launch-models.json';scope.write_text('["old"]')
            old={**source_identity(root),'passed':True};require_matching_acceptance(old,old,source_identity(root))
            scope.write_text('["new"]');now={**source_identity(root),'passed':True}
            with self.assertRaises(ValueError):require_matching_acceptance(now,old,source_identity(root))
    def test_changed_implementation_and_missing_evidence_are_rejected(self):
        with self.temporary_workspace() as d:
            root=Path(d);(root/'model').mkdir();(root/'model/commerce-launch-models.json').write_text('[]');code=root/'model/money.go';code.write_text('before')
            old={**source_identity(root),'passed':True};code.write_text('after');now={**source_identity(root),'passed':True}
            with self.assertRaises(ValueError):require_matching_acceptance(old,old,source_identity(root))
            with self.assertRaises(ValueError):require_matching_acceptance(now,{'passed':True},source_identity(root))
            require_matching_acceptance(now,now,source_identity(root))

if __name__=='__main__':unittest.main()
