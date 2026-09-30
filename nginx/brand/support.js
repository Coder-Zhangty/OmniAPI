// 用户侧在线客服页。
//
// 身份沿用控制台那一套：POST /api/user/auth/refresh 换一个短时 access_token，
// 之后所有请求带 Authorization。服务端据此自己判断消息属于谁，页面从不发送 uid。
//
// 实时推送用 fetch + ReadableStream 手读 SSE，而不是 EventSource：EventSource 不能
// 带请求头，只能把令牌塞进 URL，那会把它写进各层日志。代价是自己解析 SSE（几十行）。
(function(){
  'use strict';
  var API='/brand/api/support/';
  var POLL_FALLBACK=5000;
  var MAX_TEXT=2000;
  var accessToken=null, conversation=null, seen={}, lastSeq=0;
  var controller=null, reconnectTimer=null, polling=false, stopping=false;
  var $=function(id){return document.getElementById(id);};

  function json(url,options){
    return fetch(url,Object.assign({cache:'no-store',credentials:'same-origin'},options||{}))
      .then(function(r){
        return r.json().catch(function(){return {};}).then(function(p){
          if(r.status===401){var e=new Error('unauthorized');e.status=401;throw e;}
          if(!r.ok||p.success===false)throw new Error(p.message||('HTTP '+r.status));
          return p;
        });
      });
  }
  function auth(){
    if(!accessToken)throw new Error('尚未取得登录态');
    return {'Content-Type':'application/json','Authorization':'Bearer '+accessToken};
  }

  // 每条消息都写清是谁说的。只靠左右对齐，人多聊几轮就分不清了。
  var WHO={user:'我',operator:'客服',ai:'AI 助手（可能出错）'};

  function stampText(value){
    var date=new Date(value);
    if(isNaN(date))return '';
    // 隔天还只写时分，就分不清是哪天的事，所以换天时连日期一起写。
    return date.toDateString()===new Date().toDateString()
      ? date.toLocaleTimeString('zh-CN',{hour12:false})
      : date.toLocaleString('zh-CN',{hour12:false});
  }

  function bubble(row){
    var role=row.role||'user';
    var wrap=document.createElement('div');
    wrap.className='bubble '+role;
    if(WHO[role]){
      var label=document.createElement('span');
      label.className='who';
      label.textContent=WHO[role];
      wrap.appendChild(label);
    }
    var body=document.createElement('span');
    body.textContent=row.text||'';
    wrap.appendChild(body);
    if(row.created_at){
      var when=document.createElement('span');
      when.className='when';
      when.textContent=stampText(row.created_at);
      wrap.appendChild(when);
    }
    return wrap;
  }

  function append(rows){
    var log=$('log');
    var stick=log.scrollHeight-log.scrollTop-log.clientHeight<60;
    var added=false;
    (rows||[]).forEach(function(row){
      var seq=Number(row.seq||0);
      if(seen[seq])return;
      if(seq){seen[seq]=true;lastSeq=Math.max(lastSeq,seq);}
      log.appendChild(bubble(row));
      added=true;
    });
    if(added&&stick)log.scrollTop=log.scrollHeight;
  }

  function renderState(state){
    var node=$('conv-state');
    if(!state||state.status!=='open'){
      node.textContent='会话已结束。可以重新发起。';
      node.className='state';
      $('send').disabled=true;
      return;
    }
    if(state.needs_human){
      node.textContent='已标记为需要人工处理，客服会尽快回复。';
      node.className='state human';
    }else{
      node.textContent='消息会实时送达。';
      node.className='state';
    }
    $('send').disabled=false;
  }

  function parseBlocks(buffer){
    var blocks=buffer.split('\n\n');
    var rest=blocks.pop();
    blocks.forEach(function(block){
      var name='message', data=[];
      block.split('\n').forEach(function(line){
        if(line.indexOf('event:')===0)name=line.slice(6).trim();
        else if(line.indexOf('data:')===0)data.push(line.slice(5).trim());
      });
      if(!data.length)return;
      var payload={};
      try{payload=JSON.parse(data.join('\n'));}catch(e){return;}
      handle(name,payload);
    });
    return rest;
  }

  function handle(name,payload){
    if(name==='message')append([payload]);
    else if(name==='state'){renderState(payload);if(payload.status&&payload.status!=='open')stopping=true;}
    else if(name==='ready'&&payload.since)lastSeq=Math.max(lastSeq,Number(payload.since)||0);
  }

  function pollLoop(){
    if(polling)return;
    polling=true;
    $('conv-state').textContent='实时推送繁忙，已切换为轮询（每 5 秒）。';
    var tick=function(){
      if(stopping){polling=false;return;}
      json(API+'history?conversation_id='+encodeURIComponent(conversation)+'&since='+lastSeq,
           {headers:auth()})
        .then(function(p){append(p.messages);renderState(p);})
        .catch(function(){})
        .then(function(){if(!stopping)setTimeout(tick,POLL_FALLBACK);else polling=false;});
    };
    tick();
  }

  function readStream(response){
    var reader=response.body.getReader();
    var decoder=new TextDecoder();
    var buffer='';
    var pump=function(){
      return reader.read().then(function(chunk){
        if(chunk.done)return null;
        buffer+=decoder.decode(chunk.value,{stream:true});
        buffer=parseBlocks(buffer);
        return pump();
      });
    };
    return pump();
  }

  function connect(){
    if(stopping||!conversation)return;
    controller=new AbortController();
    fetch(API+'stream?conversation_id='+encodeURIComponent(conversation)+'&since='+lastSeq,
          {headers:auth(),cache:'no-store',credentials:'same-origin',signal:controller.signal})
      .then(function(response){
        if(response.status===503){pollLoop();return null;}
        if(response.status===401){accessToken=null;return session().then(connect);}
        if(!response.ok)throw new Error('stream '+response.status);
        return readStream(response);
      })
      .catch(function(){})
      .then(function(){
        // nginx 会在 600 秒处切断长连接，这是预期行为：带 since 重连即可补齐。
        if(stopping)return;
        clearTimeout(reconnectTimer);
        reconnectTimer=setTimeout(connect,1500);
      });
  }

  function session(){
    return fetch('/api/user/auth/refresh',{method:'POST',cache:'no-store',credentials:'same-origin'})
      .then(function(r){return r.ok?r.json():null;})
      .then(function(p){
        var data=(p&&p.data)||{};
        if(!data.access_token)return null;
        accessToken=data.access_token;
        return data.user||{};
      })
      .catch(function(){return null;});
  }

  function send(){
    var text=$('text').value;
    if(!text.trim())return;
    $('send').disabled=true;
    $('send-status').textContent='正在发送…';
    json(API+'send',{method:'POST',headers:auth(),
                     body:JSON.stringify({conversation_id:conversation,text:text})})
      .then(function(p){
        $('text').value='';
        append([p.message]);
        renderState({status:'open',needs_human:p.needs_human});
        $('send-status').textContent='';
      })
      .catch(function(error){
        $('send-status').textContent='发送失败：'+error.message;
        if(error.status===401){accessToken=null;session().then(function(){connect();});}
      })
      .then(function(){$('send').disabled=false;});
  }

  function handoff(){
    $('handoff').disabled=true;
    sendText('请转人工客服。');
  }
  function sendText(text){
    $('text').value=text;
    send();
    $('handoff').disabled=true;
  }

  function close_(){
    if(!confirm('结束本次会话？结束后可以重新发起。'))return;
    json(API+'close',{method:'POST',headers:auth(),
                      body:JSON.stringify({conversation_id:conversation})})
      .then(function(){stopping=true;renderState({status:'closed'});load().catch(function(){});})
      .catch(function(error){$('send-status').textContent=error.message;});
  }

  function show(id){['gate','closed','chat-panel'].forEach(function(name){$(name).hidden=name!==id;});}

  function load(){
    return json(API+'config').then(function(config){
      $('service-hours').textContent=config.service_hours||'';
      $('greeting').textContent=config.greeting||'';
      $('privacy').textContent=config.privacy_note||'';
      MAX_TEXT=Number(config.max_text||MAX_TEXT);
      $('text').maxLength=MAX_TEXT;
      if(!config.enabled){
        $('closed-note').textContent=config.offline_hint||'请稍后再来，或发邮件联系。';
        show('closed');
        return null;
      }
      return session().then(function(user){
        if(!user){show('gate');return null;}
        return json(API+'start',{method:'POST',headers:auth(),
                                 body:JSON.stringify({source:'support-page'})})
          .then(function(p){
            conversation=p.conversation.id;
            stopping=p.conversation.status!=='open';
            $('conv-id').textContent=conversation;
            $('log').replaceChildren();
            seen={};lastSeq=0;
            append(p.conversation.messages);
            renderState(p.conversation);
            show('chat-panel');
            if(!stopping)connect();
          });
      });
    }).catch(function(error){
      $('closed-note').textContent='客服服务暂不可用：'+error.message;
      show('closed');
    });
  }

  $('send').addEventListener('click',send);
  $('handoff').addEventListener('click',handoff);
  $('close-conv').addEventListener('click',close_);
  $('text').addEventListener('keydown',function(event){
    if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();send();}
  });
  document.addEventListener('visibilitychange',function(){
    if(document.visibilityState==='visible'&&!stopping&&conversation&&!polling){
      clearTimeout(reconnectTimer);
      connect();
    }
  });
  load();
})();
