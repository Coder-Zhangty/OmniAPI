package openai

import (
 "encoding/base64"
 "encoding/binary"
 "encoding/json"
 "errors"
 "hash/crc32"
 "strings"
 "fmt"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
)

// Validate the real PNG before the handler writes a success or settles quota.
func omniValidateImageResponse(info *relaycommon.RelayInfo, body []byte) error {
 request,ok:=info.Request.(*dto.ImageRequest)
 if !ok||len(info.OmniPriceQuote)==0 {return nil}
 var quote struct {Specs json.RawMessage `json:"image_specs"`;Restrictions []string `json:"restrictions"`}
 if json.Unmarshal(info.OmniPriceQuote,&quote)!=nil {return errors.New("图片报价无效")}
 enabled:=len(quote.Specs)>0
 for _,r:=range quote.Restrictions {enabled=enabled||strings.HasPrefix(r,"image_generation_")}
 if !enabled {return nil}
 invalid:=errors.New("上游未返回符合已验收规格的完整 PNG，请携带请求编号联系站内客服")
 var response struct {Data []struct{Base64 string `json:"b64_json"`} `json:"data"`}
 if len(body)>96<<20||json.Unmarshal(body,&response)!=nil||len(response.Data)!=1 {return invalid}
 data,err:=base64.StdEncoding.DecodeString(response.Data[0].Base64)
 if err!=nil||len(data)<33||string(data[:8])!="\x89PNG\r\n\x1a\n" {return invalid}
 width,height:=binary.BigEndian.Uint32(data[16:20]),binary.BigEndian.Uint32(data[20:24])
 if width==0||height==0||width>16384||height>16384||request.Size!=fmt.Sprintf("%dx%d",width,height) {return invalid}
 seenData:=false
 for pos:=8;pos+12<=len(data); {
  n:=uint64(binary.BigEndian.Uint32(data[pos:pos+4]));end:=uint64(pos)+12+n
  if end>uint64(len(data)) {return invalid}
  kind:=string(data[pos+4:pos+8]);chunkEnd:=int(end)
  if crc32.ChecksumIEEE(data[pos+4:chunkEnd-4])!=binary.BigEndian.Uint32(data[chunkEnd-4:chunkEnd]) {return invalid}
  if pos==8&&(kind!="IHDR"||n!=13) {return invalid}
  if kind=="IDAT" {seenData=true}
  if kind=="IEND" {if seenData&&n==0&&chunkEnd==len(data) {return nil};return invalid}
  pos=chunkEnd
 }
 return invalid
}
