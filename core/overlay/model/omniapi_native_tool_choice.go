package model

import (
 "encoding/json"
 "slices"
 "strings"
)

// Only an evidenced native restriction changes existing routing. Chat's
// validated tool-choice compatibility remains independent of native Messages.
func OmniNativeToolChoiceAllows(version,name string,id int,choice any) bool {
 encoded,err:=json.Marshal(choice);if err!=nil{return false}
 var mode struct{Type string `json:"type"`}
 if json.Unmarshal(encoded,&mode)!=nil || (mode.Type!="tool"&&mode.Type!="any") {return true}
 registry:=OmniRegistryAt(version)
 if !registry.Enabled{return true}
 for _,binding:=range registry.Models[registry.Canonical(name)].Bindings {
  if binding.ChannelID==id {return !slices.Contains(binding.Supports,"native_tool_choice_auto_only")}
 }
 return true
}

func OmniNativeToolCapabilities(tools any) []string {
 encoded,err:=json.Marshal(tools);if err!=nil{return []string{"invalid_native_tool_definition"}}
 var rows []struct{Type string `json:"type"`;Name string `json:"name"`}
 if json.Unmarshal(encoded,&rows)!=nil{return []string{"invalid_native_tool_definition"}}
 required:=[]string{}
 for _,tool:=range rows {
  capability:=""
  switch {
  case strings.HasPrefix(tool.Type,"web_search_")&&tool.Name=="web_search":capability="native_web_search"
  case strings.HasPrefix(tool.Type,"web_fetch_")&&tool.Name=="web_fetch":capability="native_web_fetch"
  case strings.HasPrefix(tool.Type,"bash_"):capability="native_bash"
  case strings.HasPrefix(tool.Type,"text_editor_"):capability="native_text_editor"
  }
  if capability!=""&&!slices.Contains(required,capability){required=append(required,capability)}
 }
 return required
}
