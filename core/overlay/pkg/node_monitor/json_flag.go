package nodemonitor

// JSONFlag observes only the top-level boolean success property. It retains no
// response bodies, nested values, personal data or credentials, even on large responses.
type JSONFlag struct {
 depth int
 quoted,escaped,keyString,wantKey,wantBoolean bool
 key,boolean string
 Found,Success bool
}
func (f *JSONFlag) Feed(data []byte) {
 for _,b:=range data {
  if f.quoted {
   if f.escaped {f.escaped=false;continue}
   if b=='\\' {f.escaped=true;continue}
   if b=='"' {f.quoted=false;continue}
   if f.keyString && len(f.key)<16 {f.key+=string(b)}
   continue
  }
  if f.wantBoolean {
   if b==' ' || b=='\n' || b=='\r' || b=='\t' {continue}
   if b>='a' && b<='z' {if len(f.boolean)<6 {f.boolean+=string(b)};continue}
   if f.boolean=="true" || f.boolean=="false" {f.Found=true;f.Success=f.boolean=="true"}
   f.wantBoolean=false;f.boolean=""
  }
  switch b {
  case '{','[': f.depth++;if f.depth==1 {f.wantKey=true}
  case '}',']': f.depth--
  case ',': if f.depth==1 {f.wantKey=true;f.key=""}
  case '"': f.quoted=true;f.keyString=f.depth==1&&f.wantKey;if f.keyString {f.key="";f.wantKey=false}
  case ':': if f.depth==1 && f.key=="success" {f.wantBoolean=true;f.boolean=""}
  }
 }
}
