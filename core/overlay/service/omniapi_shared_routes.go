package service

import relaycommon "github.com/QuantumNous/new-api/relay/common"

func OmniSharedChannelAllowed(info *relaycommon.RelayInfo,id int) bool {
 if info==nil||info.OmniPoolID==""{return true}
 _,ok:=info.OmniSharedDispatchBounds[id];return ok
}
