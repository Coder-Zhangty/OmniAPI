package service

import (
 "bytes"
 "encoding/base64"
 "errors"
 "image"
 _ "image/gif"
 _ "image/jpeg"
 _ "image/png"
 "strings"
)

func omniValidateEmbeddedImage(mime,data string) error {
 invalid:=errors.New("图片编码或媒体类型不匹配，请使用有效的 PNG、JPEG、GIF 或 WebP 图片")
 if len(data)>28<<20 {return errors.New("图片过大，请压缩后重试")}
 decoded,err:=base64.StdEncoding.DecodeString(data)
 if err!=nil||len(decoded)==0||len(decoded)>20<<20 {return invalid}
 if mime=="image/webp" {
  if len(decoded)<12||string(decoded[:4])!="RIFF"||string(decoded[8:12])!="WEBP" {return invalid}
  return nil
 }
 expected:=map[string]string{"image/png":"png","image/jpeg":"jpeg","image/gif":"gif"}[mime]
 if expected=="" {return invalid}
 config,format,err:=image.DecodeConfig(bytes.NewReader(decoded))
 if err!=nil||format!=expected||config.Width<=0||config.Height<=0 {return invalid}
 return nil
}

func omniValidateInputImages(value any) error {
 switch v:=value.(type) {
 case []any:
  for _,item:=range v {if err:=omniValidateInputImages(item);err!=nil {return err}}
 case map[string]any:
  kind,_:=v["type"].(string)
  if kind=="image" {
   if source,ok:=v["source"].(map[string]any);ok&&source["type"]=="base64" {
    mime,_:=source["media_type"].(string);data,_:=source["data"].(string)
    if err:=omniValidateEmbeddedImage(mime,data);err!=nil {return err}
   }
  }
  if kind=="image_url"||kind=="input_image" {
   url,_:=v["image_url"].(string)
   if part,ok:=v["image_url"].(map[string]any);ok {url,_=part["url"].(string)}
   if strings.HasPrefix(url,"data:") {
    header,data,ok:=strings.Cut(url,",")
    if !ok||!strings.HasSuffix(header,";base64") {return errors.New("图片 data URL 需要有效的 base64 编码")}
    mime:=strings.TrimSuffix(strings.TrimPrefix(header,"data:"),";base64")
    if err:=omniValidateEmbeddedImage(mime,data);err!=nil {return err}
   }
  }
  for _,key:=range []string{"inlineData","inline_data"} {
   if part,ok:=v[key].(map[string]any);ok {
    mime,_:=part["mimeType"].(string);if mime=="" {mime,_=part["mime_type"].(string)}
    if strings.HasPrefix(mime,"image/") {data,_:=part["data"].(string);if err:=omniValidateEmbeddedImage(mime,data);err!=nil {return err}}
   }
  }
  for _,key:=range []string{"messages","input","contents","content","parts","system","systemInstruction"} {
   if err:=omniValidateInputImages(v[key]);err!=nil {return err}
  }
 }
 return nil
}
