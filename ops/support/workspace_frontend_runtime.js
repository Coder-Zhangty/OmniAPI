  const root=surface==='admin'?'/brand/admin/api/support/':'/brand/api/support/';
  let accessToken='',csrf='',busy=false,authenticated=false,refreshing=false,filePicking=false,pollTimer,stopStream=()=>{},streamTimer,previewURL='',streamTicket='';
  const details=new Map();
  async function request(path,options={}){
    const headers={...(surface==='user'?{Authorization:'Bearer '+accessToken}:{}),...options.headers};
    if(options.method==='POST'){headers['Content-Type']='application/json';if(surface==='admin')headers['X-CSRF-Token']=csrf;}
    const response=await fetch(root+path,{...options,headers,credentials:'same-origin',cache:'no-store'});
    const payload=await response.json().catch(()=>({}));
    if(!response.ok||payload.success===false){const error=new Error(payload.message||'请求失败，请稍后重试');error.status=response.status;throw error;}
    return payload;
  }
  const post=(path,values)=>request(path,{method:'POST',body:JSON.stringify(values)});
  async function authenticate(){
    if(surface==='admin'){
      const session=await request('session');
      if(session.authenticated){csrf=session.csrf_token;authenticated=true;return;}
    }
    const response=await fetch('/api/user/auth/refresh',{method:'POST',credentials:'same-origin',cache:'no-store'});
    const payload=await response.json().catch(()=>({}));
    accessToken=payload?.data?.access_token||'';
    if(!response.ok||!accessToken){const error=new Error('请先登录 OmniAPI 控制台，再进入客服。');error.status=401;throw error;}
    if(surface==='admin'){
      await post('session',{access_token:accessToken});accessToken='';
      const session=await request('session');csrf=session.csrf_token;
      if(!session.authenticated){const error=new Error('需要管理员账号才能进入客服工作台');error.status=403;throw error;}
    }
    authenticated=true;
  }
  function fail(error){
    const text=error?.message||'操作失败，请重试';notify(text);
    const formError=$('#modal-error')||$('#composer-error');if(formError){formError.hidden=false;formError.textContent=text;}
    if(error.status===401){authenticated=false;stopStream();}
  }
  function initialError(error){
    app.innerHTML=`<main class="workspace-loading" id="main"><section class="workspace-error"><h1>${surface==='admin'?'客服工作台':'帮助与支持'}</h1><p role="alert">${esc(error.message)}</p><div class="row">${error.status===401||error.status===403?'<a class="btn primary" href="/login?redirect='+encodeURIComponent(location.pathname)+'">前往登录</a>':''}${btn('重试','retry')}<a class="btn" href="/console">返回控制台</a></div></section></main>`;
  }
  function normalizeMessage(row){return {role:row.internal?'note':({operator:'agent',ai:'agent',system:'system'}[row.role]||'user'),text:row.text||'',time:formatTime(row.created_at),seq:row.seq,files:(row.attachments||[]).map(a=>({id:a.id,name:a.name||'图片',size:a.size||0,type:a.mime}))};}
  function formatTime(value){const date=new Date(value);return Number.isNaN(date.getTime())?'—':date.toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false,month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit'});}
  function mergeDetails(){for(const t of data.tickets){const found=details.get(t.id);t.messages=found?.messages||[];t.loading=!found;}}
  function preserveRender(){
    if(filePicking)return;
    const positions=[...document.querySelectorAll('.messages,.drawer-messages,.user-tickets,.admin-sidebar')].map(e=>({selector:e.classList.contains('messages')?'.messages':e.classList.contains('drawer-messages')?'.drawer-messages':e.classList.contains('user-tickets')?'.user-tickets':'.admin-sidebar',top:e.scrollTop,atBottom:e.scrollHeight-e.scrollTop-e.clientHeight<40}));
    render();for(const item of positions){const e=$(item.selector);if(e)e.scrollTop=item.atBottom?e.scrollHeight:item.top;}
  }
  async function loadDetail(id,{markRead=false}={}){
    if(!id)return;
    const payload=await request('v2/ticket?id='+encodeURIComponent(id));
    details.set(id,{messages:(payload.messages||[]).map(normalizeMessage)});mergeDetails();preserveRender();
    if(markRead&&ticket(id)?.unread){await post(surface==='admin'?'read':'v2/read',surface==='admin'?{conversation_id:id}:{ticket_id:id});const t=ticket(id);if(t)t.unread=0;}
  }
  function syncStream(){
    const id=surface==='admin'?'all':ticket(state.userTicket)?.status==='resolved'?'':state.userTicket||'';
    if(streamTicket===id)return;stopStream();streamTicket=id;if(!id)return;
    const suffix=surface==='admin'?'':'?ticket_id='+encodeURIComponent(id);
    stopStream=window.SupportUI.stream(root+'v2/stream'+suffix,surface==='user'?{Authorization:'Bearer '+accessToken}:{},()=>{clearTimeout(streamTimer);streamTimer=setTimeout(()=>refresh().catch(fail),250);});
  }
  async function refresh({detail=true}={}){
    if(refreshing||filePicking)return;refreshing=true;
    try{
      const payload=await request('v3/workspace?days='+state.range);data=payload.data;mergeDetails();
      if(!state.userTicket||!ticket(state.userTicket))state.userTicket=data.tickets[0]?.id;
      preserveRender();
      if(detail){const id=surface==='admin'?state.drawer:state.userTicket;if(id)await loadDetail(id,{markRead:document.visibilityState==='visible'&&(surface==='admin'||!state.showList)});}
      syncStream();
    }finally{refreshing=false;}
  }
  async function boot(){
    try{
      await authenticate();await refresh();
      clearInterval(pollTimer);syncStream();
      pollTimer=setInterval(()=>{if(document.visibilityState==='visible'&&!busy&&!modal.open)refresh().catch(fail);},30000);
    }catch(error){initialError(error);}
  }
  async function write(operation){
    if(busy)return;busy=true;
    const controls=[...document.querySelectorAll('button[type=submit]')].map(e=>[e,e.disabled]);controls.forEach(([e])=>e.disabled=true);
    try{if(!authenticated)await authenticate();await operation();}
    catch(error){fail(error);}
    finally{busy=false;controls.forEach(([e,disabled])=>e.disabled=disabled);}
  }
  const catalog=(kind,values)=>post('v3/catalog',{kind,values});
  async function updateTicket(id,values){await post('v2/update',{ticket_id:id,...values});}
  async function setStatus(id,value){
    if(value==='active'){const t=ticket(id);if(!t.assignee)await updateTicket(id,{assignee_id:data.team[0]?.id||'客服'});}
    if(value==='open')await updateTicket(id,{assignee_id:''});
    await post('v2/status',{ticket_id:id,status:value==='resolved'?'resolved':value==='waiting'?'waiting_user':'waiting_operator'});
  }
  function newTeam(){showModal('添加负责人',`<p class="modal-description">用于标记和分配工单，不会授予平台管理权限。</p><form id="team-form"><div class="form-group"><label for="team-name">负责人名称</label><input class="field" id="team-name" required maxlength="30"></div><p class="form-error" id="modal-error" hidden role="alert"></p><div class="modal-actions">${btn('取消','close-modal')}<button class="btn primary" type="submit">保存</button></div></form>`);}
  async function openAttachment(target){
    const response=await fetch(root+'v2/attachment?id='+encodeURIComponent(target.dataset.id),{headers:surface==='user'?{Authorization:'Bearer '+accessToken}:{},credentials:'same-origin',cache:'no-store'});
    if(!response.ok)throw Error('图片读取失败，请重新登录后重试');
    const blob=await response.blob();if(!/^image\/(png|jpeg|webp)$/.test(blob.type))throw Error('附件格式不正确');
    if(previewURL)URL.revokeObjectURL(previewURL);previewURL=URL.createObjectURL(blob);
    showModal('图片附件',`<img class="attachment-preview" src="${previewURL}" alt="${esc(target.dataset.name)}"><div class="modal-actions"><a class="btn primary" href="${previewURL}" download="${esc(target.dataset.name)}">下载图片</a>${btn('关闭','close-modal')}</div>`);
  }
  async function uploadPending(id){
    const result=[];
    for(const file of pendingFiles){
      if(!file.attachment){
        const encoded=await new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(String(reader.result).split(',')[1]);reader.onerror=()=>reject(Error('读取图片失败'));reader.readAsDataURL(file);});
        const payload=await post('v2/upload',{ticket_id:id,files:[{name:file.name,data:encoded}]});
        file.attachment=payload.attachments[0];
      }
      result.push(file.attachment);
    }
    return result;
  }
  async function sendMessage(form,isAdmin){
    const id=form.dataset.id,t=ticket(id),text=form.querySelector('textarea').value.trim();
    if(!t||t.status==='resolved')return;
    if(!text)throw Error('请补充文字说明后发送；每条可附 3 张图片。');
    const internal=isAdmin&&Boolean(state.internalNote);
    if(internal&&pendingFiles.length)throw Error('内部备注仅支持文字，请先移除图片。');
    const attachments=await uploadPending(id);
    await post(internal?'v2/note':isAdmin?'v2/reply':'v2/message',{ticket_id:id,text,attachments});
    drafts.delete(`${isAdmin?'admin':'user'}-${id}`);pendingFiles=[];
    await refresh();const log=isAdmin?$('.drawer-messages'):$('#messages');if(log)log.scrollTop=log.scrollHeight;
    notify(internal?'内部备注已保存，仅管理员可见':'消息已发送');
  }
  async function click(e){
    const target=e.target.closest('[data-action],[data-page],[data-category-nav],[data-status],[data-user-ticket],[data-user-tab],[data-ticket],[data-toggle-category],[data-move-category]');if(!target)return;
    if(target.dataset.page){state.page=target.dataset.page;state.nav=false;state.selected.clear();preserveRender();return;}
    if(target.dataset.categoryNav){state.page='inbox';state.category=target.dataset.categoryNav;state.status='all';state.nav=false;preserveRender();return;}
    if(target.dataset.status){state.status=target.dataset.status;state.selected.clear();preserveRender();return;}
    if(target.dataset.userTicket){state.userTicket=target.dataset.userTicket;state.showList=false;pendingFiles=[];preserveRender();await loadDetail(state.userTicket,{markRead:true});syncStream();return;}
    if(target.dataset.userTab){state.userTab=target.dataset.userTab;preserveRender();return;}
    if(target.dataset.ticket){if(e.target.closest('input'))return;state.drawer=target.dataset.ticket;state.internalNote=false;pendingFiles=[];preserveRender();await loadDetail(state.drawer,{markRead:true});$('.drawer [data-action=close-drawer]')?.focus();return;}
    if(target.dataset.toggleCategory){await write(async()=>{const c=category(target.dataset.toggleCategory);await catalog('category_toggle',{id:c.id,enabled:!c.enabled});await refresh({detail:false});notify('分类状态已保存，历史工单保留');});return;}
    if(target.dataset.moveCategory){await write(async()=>{await catalog('category_move',{id:target.dataset.moveCategory,direction:Number(target.dataset.direction)});await refresh({detail:false});});return;}
    switch(target.dataset.action){
      case 'retry':await boot();break;
      case 'toggle-nav':state.nav=!state.nav;preserveRender();break;
      case 'new-category':categoryForm();break;
      case 'edit-category':categoryForm(target.dataset.id);break;
      case 'new-reply':replyForm();break;
      case 'edit-reply':replyForm(target.dataset.id);break;
      case 'new-team':newTeam();break;
      case 'new-ticket':if(!data.enabled)throw Error('客服暂时关闭，请稍后再试');newTicket();break;
      case 'close-modal':closeModal();if(previewURL){URL.revokeObjectURL(previewURL);previewURL='';}break;
      case 'all-tickets':state.page='inbox';preserveRender();break;
      case 'clear-filters':state.query='';state.category='all';state.status='all';preserveRender();break;
      case 'clear-user-filter':state.userQuery='';state.userCategory='all';preserveRender();break;
      case 'close-drawer-background':if(e.target!==target)break;state.drawer=null;pendingFiles=[];preserveRender();break;
      case 'close-drawer':state.drawer=null;pendingFiles=[];preserveRender();break;
      case 'show-user-list':state.showList=true;preserveRender();break;
      case 'clear-files':pendingFiles=[];preserveRender();break;
      case 'user-view':location.href='support.html';break;
      case 'resolve-user':case 'reopen-user':await write(async()=>{await post('v2/status',{ticket_id:target.dataset.id,action:target.dataset.action==='resolve-user'?'resolve':'reopen'});state.userTab=target.dataset.action==='resolve-user'?'resolved':'active';await refresh();notify('咨询状态已更新');});break;
      case 'help':{const item=helpItems[Number(target.dataset.id)||0];showModal(item[0],`<div class="help-body">${esc(item[1])}</div><div class="modal-actions">${btn('我知道了','close-modal','','primary')}</div>`);break;}
      case 'notifications':showModal('待办提醒',`<div class="help-body">待回复 ${count('open')} 条\n高优先级 ${data.tickets.filter(t=>t.status==='open'&&t.priority==='high').length} 条\n待分配 ${data.tickets.filter(t=>t.status!=='resolved'&&!t.assignee).length} 条</div><div class="modal-actions">${btn('关闭','close-modal')}</div>`);break;
      case 'bulk-assign':await write(async()=>{const value=$('#bulk-assignee').value;if(!value)throw Error('请先选择负责人');const ids=[...state.selected];for(const id of ids){await updateTicket(id,{assignee_id:value});state.selected.delete(id);}await refresh({detail:false});notify('所选工单已分配');});break;
      case 'bulk-resolve':await write(async()=>{for(const id of [...state.selected]){await setStatus(id,'resolved');state.selected.delete(id);}await refresh({detail:false});notify('所选工单已解决');});break;
      case 'export':exportCSV();break;
      case 'attachment':await openAttachment(target);break;
    }
  }
  document.addEventListener('click',e=>{if(e.target.closest('label.attach')||e.target.id==='message-images')filePicking=true;},true);
  document.addEventListener('cancel',e=>{if(e.target.id==='message-images'){filePicking=false;preserveRender();}},true);
  document.addEventListener('click',e=>{click(e).catch(fail);});
  document.addEventListener('input',e=>{const t=e.target;if(t.dataset.draft){drafts.set(t.dataset.draft,t.value);return;}if(t.id==='user-search'){state.userQuery=t.value;preserveRender();}if(t.id==='global-search'){state.query=t.value;state.page='inbox';preserveRender();}if(t.id==='inbox-search'){state.query=t.value;preserveRender();}});
  async function change(t){
    if(t.id==='internal-note'){state.internalNote=t.checked;}
    if(t.id==='date-range'){state.range=Number(t.value);await refresh({detail:false});}
    if(t.id==='admin-category-filter'){state.category=t.value;preserveRender();}
    if(t.id==='user-category-filter'){state.userCategory=t.value;preserveRender();}
    if(t.dataset.select){t.checked?state.selected.add(t.dataset.select):state.selected.delete(t.dataset.select);preserveRender();}
    if(t.id==='select-all'){const rows=state.page==='overview'?filteredTickets().slice(0,5):filteredTickets();rows.forEach(r=>t.checked?state.selected.add(r.id):state.selected.delete(r.id));preserveRender();}
    if(t.id==='ticket-category'){await write(async()=>{await post('v3/category',{ticket_id:state.userTicket,category:t.value});await refresh();notify('咨询分类已更新');});}
    if(['drawer-category','drawer-assignee','drawer-status'].includes(t.id)){await write(async()=>{if(t.id==='drawer-status')await setStatus(state.drawer,t.value);else await updateTicket(state.drawer,{[t.id==='drawer-category'?'category':'assignee_id']:t.value});await refresh();notify('工单信息已保存');});}
    if(t.id==='reply-template'&&t.value){const r=data.replies.find(r=>r.id===Number(t.value));if(r){drafts.set(`admin-${state.drawer}`,r.text);preserveRender();$('#admin-message')?.focus();}}
    if(t.id==='message-images'){filePicking=false;const files=[...t.files];if(files.length+pendingFiles.length>3||files.some(f=>!['image/png','image/jpeg','image/webp'].includes(f.type)||f.size>5*1024*1024)){t.value='';throw Error('最多 3 张 PNG、JPEG 或 WebP 图片，每张不超过 5 MiB');}pendingFiles.push(...files);preserveRender();}
  }
  document.addEventListener('change',e=>{change(e.target).catch(fail);});
  document.addEventListener('submit',e=>{
    const form=e.target;if(!['category-form','reply-form','team-form','new-ticket-form','settings-form','user-message-form','admin-message-form'].includes(form.id))return;e.preventDefault();if(!form.reportValidity())return;
    write(async()=>{
      if(form.id==='category-form'){await catalog('category',{id:form.dataset.id||undefined,name:$('#category-name').value.trim(),color:$('#category-color').value,sla:Number($('#category-sla').value),priority:$('#category-priority').value});closeModal();await refresh({detail:false});notify('分类已保存');}
      if(form.id==='reply-form'){await catalog('reply',{id:Number(form.dataset.id)||undefined,title:$('#reply-title').value.trim(),text:$('#reply-text').value.trim()});closeModal();await refresh({detail:false});notify('快捷回复已保存');}
      if(form.id==='team-form'){await catalog('team',{name:$('#team-name').value.trim()});closeModal();await refresh({detail:false});notify('负责人已添加');}
      if(form.id==='new-ticket-form'){const payload=await post('v2/tickets',{category:$('#new-category').value,subject:$('#new-subject').value.trim(),description:$('#new-description').value.trim(),source:'support-workspace'});state.userTicket=payload.ticket.id;state.userTab='active';state.userCategory='all';state.userQuery='';state.showList=false;closeModal();await refresh();notify('咨询已提交');}
      if(form.id==='settings-form'){const values={greeting:$('#greeting').value.trim(),service_hours:$('#hours').value.trim()},sla=Number($('#sla').value);await catalog('sla',{sla});await post('v2/settings',values);await refresh({detail:false});notify('服务设置已保存');}
      if(form.id==='user-message-form'||form.id==='admin-message-form')await sendMessage(form,form.id==='admin-message-form');
    });
  });
  document.addEventListener('keydown',e=>{
    if(e.key==='Escape'){if(state.drawer){state.drawer=null;pendingFiles=[];preserveRender();}if(state.nav){state.nav=false;preserveRender();}}
    if(e.target.matches?.('[data-ticket]')&&(e.key==='Enter'||e.key===' ')){e.preventDefault();e.target.click();}
    if(e.target.dataset?.draft&&e.key==='Enter'&&!e.shiftKey&&!e.isComposing&&e.keyCode!==229){e.preventDefault();e.target.closest('form').requestSubmit();}
    if(state.drawer&&e.key==='Tab'){const items=[...$('.drawer').querySelectorAll('button,select,textarea,a[href],input')].filter(el=>!el.disabled&&el.getClientRects().length),first=items[0],last=items.at(-1);if(e.shiftKey&&document.activeElement===first){e.preventDefault();last?.focus();}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first?.focus();}}
  });
  document.addEventListener('pointerover',e=>{const p=e.target.closest('[data-chart-tip]');if(p&&$('#chart-tip'))$('#chart-tip').textContent=p.dataset.chartTip;});
  document.addEventListener('focusin',e=>{if(e.target.dataset.chartTip&&$('#chart-tip'))$('#chart-tip').textContent=e.target.dataset.chartTip;});
  modal.addEventListener('click',e=>{if(e.target===modal){const r=modal.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom)closeModal();}});
  window.addEventListener('pagehide',()=>{stopStream();clearInterval(pollTimer);if(previewURL)URL.revokeObjectURL(previewURL);});
  boot();
})();
