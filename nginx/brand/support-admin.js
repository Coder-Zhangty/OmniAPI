// 经营者客服工作台。
//
// 与充值核验页共用同一张经营者 cookie（两个服务共用一份签名密钥），所以登录一次就能
// 同时核验充值和回客服消息。写操作要带 Origin 与 X-CSRF-Token，令牌从会话接口拿。
//
// 移动端优先：收到手机推送后点链接直接落到某个会话（#会话编号）。
(function(){
  'use strict';
  var API='/brand/admin/api/support/';
  var QUICK=['收到，我看一下。','请把充值申请编号发我，我核对一下。','这个问题我看一下，稍后回你。','已经处理好了，刷新页面看看。'];
  var csrfToken='', current=null, currentRow=null, seen={}, queue=[], lastSeq=0, controller=null;
  // 每一行都标出是谁说的：只靠左右对齐，聊上十几轮就分不清哪句是顾客、哪句是自己。
  var ROLE_TAG={user:'客户',operator:'我',ai:'AI 助手'};
  var triedBootstrap=false;
  var $=function(id){return document.getElementById(id);};

  function call(path,options){
    var opts=Object.assign({cache:'no-store',credentials:'same-origin'},options||{});
    if(opts.method&&opts.method!=='GET'){
      opts.headers=Object.assign({'Content-Type':'application/json','X-CSRF-Token':csrfToken},opts.headers||{});
    }
    return fetch(API+path,opts).then(function(response){
      return response.json().catch(function(){return {};}).then(function(payload){
        if(response.status===401){var e=new Error('unauthorized');e.status=401;throw e;}
        if(!response.ok||payload.success===false){
          var error=new Error(payload.message||('HTTP '+response.status));
          error.status=response.status;error.code=payload.code||'';
          throw error;
        }
        return payload;
      });
    });
  }
  function post(path,body){return call(path,{method:'POST',body:JSON.stringify(body||{})});}
  function when(value){
    if(!value)return '';
    var date=new Date(value);
    return isNaN(date)?'':date.toLocaleString('zh-CN',{hour12:false});
  }
  function clock(value){
    var date=new Date(value);
    return isNaN(date)?'':date.toLocaleTimeString('zh-CN',{hour12:false});
  }

  function senderLabel(role){
    if(role==='operator')return '我（客服）';
    if(role==='ai')return 'AI 助手（可能出错）';
    if(role==='system')return '';
    return '客户'+(currentRow&&currentRow.username?' · '+currentRow.username:'');
  }

  function bubble(row){
    var role=row.role||'user';
    var wrap=document.createElement('div');
    wrap.className='bubble '+role;
    var label=senderLabel(role);
    if(label){
      var who=document.createElement('span');
      who.className='who';who.textContent=label;
      wrap.appendChild(who);
    }
    var body=document.createElement('span');
    body.textContent=row.text||'';
    wrap.appendChild(body);
    if(row.created_at){
      var stamp=document.createElement('span');
      stamp.className='when';stamp.textContent=clock(row.created_at);
      wrap.appendChild(stamp);
    }
    return wrap;
  }

  function preview(row){
    var text=String(row.last_text||'').trim();
    if(!text)return '（还没有消息）';
    var tag=ROLE_TAG[row.last_role];
    if(text.length>42)text=text.slice(0,42)+'…';
    return (tag?tag+'：':'')+text;
  }
  function append(rows){
    var log=$('log');
    var stick=log.scrollHeight-log.scrollTop-log.clientHeight<60;
    var added=false;
    (rows||[]).forEach(function(row){
      var seq=Number(row.seq||0);
      if(!seq||seen[seq])return;
      seen[seq]=true;lastSeq=Math.max(lastSeq,seq);
      log.appendChild(bubble(row));added=true;
    });
    if(added&&stick)log.scrollTop=log.scrollHeight;
  }

  function renderQueue(){
    var box=$('queue');
    box.replaceChildren();
    queue.forEach(function(row){
      var item=document.createElement('button');
      item.type='button';
      item.className='item'+(row.id===current?' active':'')+(row.needs_human?' urgent':'');
      var who=document.createElement('span');
      who.className='who';
      who.textContent=(row.username||('用户 '+row.uid))+(row.needs_human?' · 待人工':'');
      var meta=document.createElement('span');
      meta.className='meta';
      // 列表里直接给出「最后一句是谁说的、说了什么」，不然得逐个点开才知道顾客要什么。
      meta.textContent=preview(row);
      var sub=document.createElement('span');
      sub.className='sub';
      sub.textContent=row.id+' · '+when(row.last_message_at);
      item.append(who,meta,sub);
      if(row.unread_operator)item.appendChild(pill(row.unread_operator+' 条未读'));
      item.addEventListener('click',function(){open(row.id);});
      box.appendChild(item);
    });
    $('queue-empty').hidden=queue.length>0;
  }
  function pill(text){
    var span=document.createElement('span');
    span.className='pill pending';span.textContent=text;return span;
  }

  function open(convId){
    current=convId;lastSeq=0;seen={};
    currentRow=queue.filter(function(item){return item.id===convId;})[0]||null;
    $('log').replaceChildren();
    $('thread-title').textContent=(currentRow&&currentRow.username
      ?currentRow.username+' · ':'对话 ')+convId;
    $('thread-meta').textContent='正在加载…';
    $('send').disabled=false;$('close-conv').disabled=false;
    location.hash=convId;
    return call('history?conversation_id='+encodeURIComponent(convId))
      .then(function(payload){
        append(payload.messages);
        $('thread-meta').textContent='最近更新 '+when((payload.messages.slice(-1)[0]||{}).created_at);
        return post('read',{conversation_id:convId});
      })
      .then(function(){return load();})
      .catch(function(error){$('thread-meta').textContent='打开失败：'+error.message;});
  }

  function load(){
    return call('conversations').then(function(payload){
      queue=payload.conversations||[];
      $('open-count').textContent=String(payload.counts.open||0);
      $('counts').textContent='进行中 '+payload.counts.open+' · 待人工 '+payload.counts.needs_human+
        ' · 未读 '+payload.counts.unread_operator;
      renderQueue();
      var wanted=location.hash.replace('#','');
      if(!current&&wanted&&queue.some(function(row){return row.id===wanted;}))return open(wanted);
      if(current&&!queue.some(function(row){return row.id===current;}))current=null;
      return null;
    });
  }

  function send(){
    var text=$('text').value;
    if(!current||!text.trim())return;
    $('send').disabled=true;
    post('reply',{conversation_id:current,text:text})
      .then(function(payload){
        $('text').value='';$('send-status').textContent='';
        append([payload.message]);
      })
      .catch(function(error){$('send-status').textContent='发送失败：'+error.message;})
      .then(function(){$('send').disabled=false;});
  }

  function closeConversation(){
    if(!current||!confirm('结束会话 '+current+'？顾客可以重新发起。'))return;
    post('close',{conversation_id:current,reason:'handled'})
      .then(function(){$('send-status').textContent='会话已结束。';current=null;
        $('send').disabled=true;$('close-conv').disabled=true;return load();})
      .catch(function(error){$('send-status').textContent=error.message;});
  }

  function handle(name,payload){
    if(name==='conversations'){
      queue=payload.conversations||[];
      $('open-count').textContent=String((payload.counts||{}).open||0);
      var counts=payload.counts||{};
      $('counts').textContent='进行中 '+counts.open+' · 待人工 '+counts.needs_human+
        ' · 未读 '+counts.unread_operator;
      renderQueue();
    }else if(name==='message'){
      if(payload.conv===current)append([payload]);
      else if(payload.role==='user'){
        var row=queue.filter(function(item){return item.id===payload.conv;})[0];
        if(row)row.unread_operator=(row.unread_operator||0)+1;
        renderQueue();
      }
    }
  }

  function connect(){
    controller=new AbortController();
    fetch(API+'stream',{cache:'no-store',credentials:'same-origin',signal:controller.signal})
      .then(function(response){
        if(response.status===401){showGate();return null;}
        if(!response.ok)throw new Error('stream '+response.status);
        var reader=response.body.getReader(),decoder=new TextDecoder(),buffer='';
        var pump=function(){
          return reader.read().then(function(chunk){
            if(chunk.done)return null;
            buffer+=decoder.decode(chunk.value,{stream:true});
            var blocks=buffer.split('\n\n');
            buffer=blocks.pop();
            blocks.forEach(function(block){
              var name='message',data=[];
              block.split('\n').forEach(function(line){
                if(line.indexOf('event:')===0)name=line.slice(6).trim();
                else if(line.indexOf('data:')===0)data.push(line.slice(5).trim());
              });
              if(!data.length)return;
              try{handle(name,JSON.parse(data.join('\n')));}catch(e){}
            });
            return pump();
          });
        };
        return pump();
      })
      .catch(function(){})
      .then(function(){setTimeout(function(){if(!document.hidden)connect();},2000);});
  }

  function showGate(){$('gate').hidden=false;$('board').hidden=true;}
  function showBoard(){$('gate').hidden=true;$('board').hidden=false;}

  function bootstrap(){
    return fetch('/api/user/auth/refresh',{method:'POST',cache:'no-store',credentials:'same-origin'})
      .then(function(response){return response.ok?response.json():null;})
      .then(function(payload){
        var data=(payload&&payload.data)||{},user=data.user||{};
        if(!data.access_token||Number(user.role||0)<10)return false;
        return post('session',{access_token:data.access_token}).then(function(){return true;});
      })
      .catch(function(){return false;});
  }

  function enter(){
    return call('session').then(function(payload){
      csrfToken=payload.csrf_token||'';
      if(payload.authenticated){
        showBoard();
        return call('settings').then(function(settings){
          $('settings').textContent='推送通道 '+((settings.notify||{}).channel||'none')+
            ' · 推送凭据 '+(settings.notify_credentials_present?'已配置':'未配置')+
            ' · AI 先答 '+((settings.ai||{}).enabled?'已开启':'未开启')+
            ' · 保留 '+(settings.retention_days||'—')+' 天 · 服务时间 '+(settings.service_hours||'未填写');
        }).then(load).then(connect);
      }
      // 只补种一次：万一补种"成功"但会话仍然不被接受（例如 cookie 没被带上），
      // 再进这个分支就会无限递归。宁可停在门口让人看清楚，也不要转圈转到内存耗尽。
      if(triedBootstrap){
        $('gate-status').textContent='会话仍然无效。请确认已登录控制台，然后刷新本页。';
        showGate();
        return null;
      }
      triedBootstrap=true;
      return bootstrap().then(function(ok){
        if(ok)return enter();
        $('gate-status').textContent='未检测到管理员登录态。请先登录控制台，再回到本页。';
        showGate();
        return null;
      });
    }).catch(function(){
      $('gate-status').textContent='无法连接客服服务。';
      showGate();
    });
  }

  $('console-login').addEventListener('click',function(){
    $('gate-status').textContent='正在核对控制台身份…';
    bootstrap().then(function(ok){
      if(ok)return enter();
      $('gate-status').textContent='控制台当前不是管理员登录态，请先登录控制台。';
      return null;
    });
  });
  $('send').addEventListener('click',send);
  $('close-conv').addEventListener('click',closeConversation);
  $('refresh').addEventListener('click',function(){load().catch(function(error){$('send-status').textContent=error.message;});});
  $('text').addEventListener('keydown',function(event){
    if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();send();}
  });
  QUICK.forEach(function(text){
    var button=document.createElement('button');
    button.type='button';button.textContent=text;
    button.addEventListener('click',function(){$('text').value=text;$('text').focus();});
    $('quick').appendChild(button);
  });
  document.addEventListener('visibilitychange',function(){
    if(document.visibilityState==='visible'&&!$('board').hidden)load().catch(function(){});
  });
  enter();
})();
