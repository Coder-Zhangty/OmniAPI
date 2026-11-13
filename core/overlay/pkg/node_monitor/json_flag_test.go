package nodemonitor
import "testing"
func TestWebsiteBusinessFailureOnlyObservesTopLevelSuccess(t *testing.T) {
 for _,tc:=range []struct{body string;found,success bool}{
  {`{"data":{"success":false},"success":true}`,true,true},
  {`{"message":"quoted \\"success\\":false","success":false}`,true,false},
  {`{"data":{"success":false}}`,false,false},
  {`{"success":false,"message":"denied"}`,true,false},
 } {
  var flag JSONFlag
  for _,b:=range []byte(tc.body){flag.Feed([]byte{b})}
  if flag.Found!=tc.found || flag.Success!=tc.success {t.Fatalf("incorrect top-level success: %+v",flag)}
 }
}
