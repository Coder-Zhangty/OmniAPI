(() => {
 'use strict';const $=id=>document.getElementById(id),api=(...args)=>window.OmniCommerce.request(...args);let busy=false;
 function el(tag,text){const e=document.createElement(tag);if(text!==undefined)e.textContent=String(text);return e;}
 function lock(value){busy=value;document.querySelectorAll('button').forEach(b=>b.disabled=value);}
 async function loadExits(){const rows=(await api('/api/omni/admin/commerce/exits')).data;$('exits').replaceChildren();if(!rows.length){$('exits').textContent='暂无申请。';return;}
  const t=el('table'),head=el('tr');for(const title of ['申请','用户','锁定内部额度','状态','处理'])head.append(el('th',title));t.append(head);
  for(const row of rows){const tr=el('tr');for(const value of [row.id,row.user_id,row.refund_quota,row.state])tr.append(el('td',value));const actions=el('td');if(row.state==='pending')for(const [action,label] of [['approve','批准原额退款'],['reject','驳回并恢复权益']]){const b=el('button',label);b.className='quiet';b.onclick=async()=>{if(busy)return;const note=prompt('请填写已核对的原付款段及处理理由：');if(!note)return;if(!confirm(`${label}？待退内部额度 ${row.refund_quota}，此操作会写入财务与审计记录。`))return;lock(true);try{await api('/api/omni/admin/commerce/exits/'+encodeURIComponent(row.id),{user_id:row.user_id,action,note});await loadExits();$('message').textContent='处理完成。';}catch(e){$('message').textContent=e.message;}finally{lock(false);}};actions.append(b);}tr.append(actions);t.append(tr);}$('exits').append(t);
 }
 document.querySelectorAll('[data-action]').forEach(button=>button.onclick=async()=>{
  if(busy||!$('migration-form').reportValidity())return;const action=button.dataset.action;
  if(action==='apply'&&!$('preview-hash').value){$('message').textContent='请先生成并核对迁移预览。';return;}
  if(action!=='preview'&&!confirm(button.textContent+'？请确认数据库备份、请求排空及能力核验记录均已完成。'))return;
  lock(true);try{const r=await api('/api/omni/admin/commerce/migration',{action,id:$('batch').value,profile_id:$('profile').value,preview_hash:$('preview-hash').value,acceptance_id:$('acceptance').value});if(action==='preview'){const b=r.data;$('preview-hash').value=b.preview_hash;const detail=(await api('/api/omni/admin/commerce/migration/'+encodeURIComponent(b.id))).data;let preview;try{preview=JSON.parse(detail.preview);}catch{preview=detail.preview;}$('preview').textContent=JSON.stringify({batch:b,preview},null,2);}else $('message').textContent='操作完成：'+action;}catch(e){$('message').textContent=e.message;}finally{lock(false);}
 });
 $('refresh-exits').onclick=()=>loadExits().catch(e=>{$('message').textContent=e.message;});
 (async()=>{try{const user=await window.OmniCommerce.identity();if(user.role<100)throw Error('需要最高管理员权限。');lock(false);await loadExits();$('message').textContent='当前界面不会自动执行迁移或开售。';}catch(e){lock(true);$('message').textContent=e.message;}})();
})();
