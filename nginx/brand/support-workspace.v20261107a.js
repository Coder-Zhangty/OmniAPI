/* Authenticated support workspace. No demo data or localStorage credentials. */
(() => {
  'use strict';
  const surface=document.body.dataset.surface;
  const $=s=>document.querySelector(s), app=$('#app'), modal=$('#modal');
  const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const paths={home:'m3 10 9-7 9 7M5 9v12h14V9M9 21v-7h6v7',inbox:'m4 4-2 12v4h20v-4L20 4H4Zm-2 12h6l2 3h4l2-3h6',tag:'M20 13 11 22 2 13V3h10l9 9-1 1ZM7 7h.01',bolt:'m13 2-9 12h7l-1 8 10-12h-7l1-8Z',book:'M12 5C8 2 4 3 2 4v16c4-2 7-1 10 1 3-2 6-3 10-1V4c-4-2-7-1-10 1Zm0 0v16',users:'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M16 4a4 4 0 0 1 0 8M22 21v-2a4 4 0 0 0-3-3.87M13 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0',settings:'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8ZM10 2h4l1 3 3 1 3-1 2 4-2 2v3l2 2-2 4-3-1-3 1-1 3h-4l-1-3-3-1-3 1-2-4 2-2v-3L1 9l2-4 3 1 3-1 1-3Z',search:'m21 21-5-5M18 10A8 8 0 1 1 2 10a8 8 0 0 1 16 0',bell:'M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9ZM10 21h4',plus:'M12 5v14M5 12h14',chevron:'m9 5 7 7-7 7',down:'m6 9 6 6 6-6',up:'m6 15 6-6 6 6',back:'M20 12H4m6-6-6 6 6 6',calendar:'M3 6h18v15H3V6ZM7 3v6M17 3v6M3 11h18',export:'M12 16V2m-5 5 5-5 5 5M3 12v9h18v-9',info:'M12 11v6M12 7h.01M22 12A10 10 0 1 1 2 12a10 10 0 0 1 20 0',user:'M20 21a8 8 0 0 0-16 0M16 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0',headset:'M3 14v-3a9 9 0 0 1 18 0v3M3 12h4v8H3v-8Zm14 0h4v8h-4v-8ZM17 20l-3 2h-3',attach:'m21 11-9 9a6 6 0 0 1-8-8L14 2a4 4 0 0 1 6 6L9 19a2 2 0 0 1-3-3l10-10',image:'M3 3h18v18H3V3Zm0 14 6-7 5 5 3-3 4 5M16 7h.01',shield:'m12 2 9 4v6c0 5-9 10-9 10S3 17 3 12V6l9-4Zm-4 10 3 3 5-6',check:'m5 12 4 4L19 6',close:'m6 6 12 12M6 18 18 6',filter:'M3 4h18l-7 8v8l-4-2v-6L3 4Z',menu:'M4 6h16M4 12h16M4 18h16',clock:'M12 6v6l4 2M22 12A10 10 0 1 1 2 12a10 10 0 0 1 20 0',send:'m22 2-7 20-4-9-9-4L22 2ZM11 13l11-11'};
  const icon=(name,cls='')=>`<svg class="icon ${cls}" viewBox="0 0 24 24" aria-hidden="true"><path d="${paths[name]||paths.info}"/></svg>`;
  const mark='<span class="brand-mark" aria-hidden="true"><svg viewBox="0 0 36 36" fill="none"><path d="M27 11A11 11 0 1 0 27 25" stroke="white" stroke-width="4.5" stroke-linecap="round"/><path d="M18 18h12l-3 6" stroke="white" stroke-width="4.5" stroke-linecap="round" stroke-linejoin="round"/></svg></span>';
  const brand=(sub=false)=>`<div class="brand">${mark}<div>OmniAPI${sub?'<small>客服中心</small>':''}</div></div>`;
  const avatar=(agent=false)=>`<span class="avatar ${agent?'agent':''}">${icon(agent?'headset':'user')}</span>`;
  const btn=(text,action,extra='',cls='')=>`<button type="button" class="btn ${cls}" data-action="${action}" ${extra}>${text}</button>`;
  const statuses={open:'待回复',active:'处理中',waiting:'等待用户',resolved:'已解决'};
  const userStatus=t=>t.status==='resolved'?'已解决':t.status==='waiting'?'待你补充':'处理中';
  const statusTag=t=>`<span class="tag ${t.status==='resolved'?'green':t.status==='waiting'?'':'blue'}">${surface==='user'?userStatus(t):statuses[t.status]}</span>`;
  let data={categories:[],tickets:[],replies:[],team:[],settings:{},analytics:{series:[]}};
  const state={page:'overview',query:'',category:'all',status:'open',range:7,selected:new Set(),drawer:null,userTicket:null,userTab:'active',userQuery:'',userCategory:'all',showList:false,nav:false};
  const drafts=new Map();let pendingFiles=[],toastTimer,lastFocus=null;
  const category=id=>data.categories.find(c=>c.id===id)||{id,name:'历史分类',color:'#a3adbc',enabled:false};
  const own=()=>data.tickets;
  const ticket=id=>data.tickets.find(t=>t.id===String(id));
  const count=status=>data.tickets.filter(t=>t.status===status).length;
  function notify(text){$('#toast').textContent=text;$('#toast').hidden=false;clearTimeout(toastTimer);toastTimer=setTimeout(()=>$('#toast').hidden=true,3400);}
  function options(selected,all=false){return (all?'<option value="all">全部分类</option>':'')+data.categories.filter(c=>c.enabled||c.id===selected).map(c=>`<option value="${esc(c.id)}" ${c.id===selected?'selected':''}>${esc(c.name)}${c.enabled?'':'（已停用）'}</option>`).join('');}
  function sidebar(){
    const links=[['overview','home','服务概览'],['inbox','inbox','工单收件箱'],['categories','tag','分类管理'],['replies','bolt','快捷回复'],['knowledge','book','知识库'],['team','users','团队与分配'],['settings','settings','服务设置']];
    return `<aside class="admin-sidebar">${brand(true)}<nav class="nav-group" aria-label="客服管理导航">${links.map(([id,i,label],index)=>`${index===5?'<div class="sidebar-rule"></div>':''}<button class="nav-item ${state.page===id?'active':''}" data-page="${id}" ${state.page===id?'aria-current="page"':''}>${icon(i)}${label}${id==='inbox'?`<span class="count-red">${count('open')}</span>`:''}</button>`).join('')}</nav><div class="sidebar-rule"></div><div class="sidebar-label">问题分类<button class="icon-btn" data-action="new-category" aria-label="新增分类">${icon('plus')}</button></div><div>${data.categories.filter(c=>c.enabled).map(c=>`<button class="category-nav ${state.page==='inbox'&&state.category===c.id?'active':''}" data-category-nav="${esc(c.id)}"><span class="dot" style="background:${esc(c.color)}"></span>${esc(c.name)}</button>`).join('')}</div><div class="sidebar-bottom"><div class="sidebar-profile"><span class="avatar admin">${icon('user')}</span><span class="grow">管理员</span>${icon('down')}</div><a class="nav-item" href="support.html">${icon('back')}查看用户界面</a></div></aside>`;
  }
  function header(title,subtitle,actions=''){return `<header class="admin-header"><div><h1>${title}</h1><p>${subtitle}</p></div>${actions?`<div class="header-actions">${actions}</div>`:''}</header>`;}
  function series(){return data.analytics.series||[];}
  function chart(){
    const rows=series(),max=Math.ceil(Math.max(...rows.map(r=>r.new),30)/10)*10,W=550,H=190,left=30,right=18,top=20,bottom=32;
    const x=i=>left+(W-left-right)*i/(rows.length-1),y=v=>top+(H-top-bottom)*(1-v/max),line=key=>rows.map((r,i)=>`${i?'L':'M'}${x(i).toFixed(2)},${y(r[key]).toFixed(2)}`).join(' ');
    const labels=rows.map((r,i)=>((rows.length<=7||i%5===0||i===rows.length-1)?`<text x="${x(i)}" y="${H-7}" text-anchor="${i===0?'start':i===rows.length-1?'end':'middle'}">${r.label}</text>`:'')).join('');
    const last=rows.at(-1);
    return `<svg class="trend-svg" viewBox="0 0 ${W} ${H}" role="img" aria-label="最近 ${state.range} 天新增和已解决咨询趋势。新增合计 ${rows.reduce((s,r)=>s+r.new,0)}，已解决 ${rows.reduce((s,r)=>s+r.solved,0)}。">${[0,1,2,3].map(i=>{const val=i*max/3;return `<line class="grid-line" x1="${left}" y1="${y(val)}" x2="${W-right}" y2="${y(val)}"/><text x="${left-11}" y="${y(val)+4}" text-anchor="end">${Math.round(val)}</text>`;}).join('')}<path class="trend-line resolved-line" d="${line('solved')}"/><path class="trend-line" d="${line('new')}"/>${rows.map((r,i)=>`<circle class="chart-point gray" cx="${x(i)}" cy="${y(r.solved)}" r="3"><title>${r.label} 已解决 ${r.solved}</title></circle><circle class="chart-point" tabindex="0" cx="${x(i)}" cy="${y(r.new)}" r="3" data-chart-tip="${r.label}：新增 ${r.new}，已解决 ${r.solved}" aria-label="${r.label} 新增 ${r.new} 已解决 ${r.solved}"><title>${r.label} 新增 ${r.new}</title></circle>`).join('')}${labels}<rect x="${x(rows.length-1)-12}" y="${y(last.new)-27}" width="25" height="19" rx="4" fill="#007aff"/><text x="${x(rows.length-1)+.5}" y="${y(last.new)-13}" text-anchor="middle" style="fill:white;font-size:11px">${last.new}</text></svg>`;
  }
  function overview(){
    const a=data.analytics,groups=data.categories.map(c=>({...c,n:data.tickets.filter(t=>t.status==='open'&&t.category===c.id).length})).filter(c=>c.n||c.enabled).sort((a,b)=>b.n-a.n),largest=Math.max(1,...groups.map(c=>c.n));
    return header('客服工作台','先处理需要回复的咨询，再查看服务表现',`<label class="sr-only" for="date-range">统计时间</label><select id="date-range"><option value="7" ${state.range===7?'selected':''}>最近 7 天</option><option value="30" ${state.range===30?'selected':''}>最近 30 天</option></select>${btn(icon('export')+'导出报表','export')}`)+`<section class="metrics" aria-label="服务概览指标"><div class="metric"><div class="metric-label">待回复</div><div class="metric-line"><span class="metric-value">${count('open')}</span><span class="tag amber">${data.tickets.filter(t=>t.status==='open'&&t.priority==='high').length} 条高优先级</span></div></div><div class="metric"><div class="metric-label">今日新增</div><div class="metric-value">${a.newToday??0}</div></div><div class="metric"><div class="metric-label">平均首次响应</div><div class="metric-value">${a.firstResponseMinutes??'—'} <span class="unit">${a.firstResponseMinutes==null?'':'分钟'}</span></div></div><div class="metric"><div class="metric-label">解决率</div><div class="metric-value">${a.resolutionRate==null?'—':a.resolutionRate+'%'}</div></div></section><section class="analytics" aria-label="咨询分析"><div class="panel chart-panel"><div class="panel-heading"><h2 class="section-title">咨询趋势</h2><div class="chart-legend"><span class="legend-item"><i class="dot" style="background:#007aff"></i>新增</span><span class="legend-item"><i class="dot" style="background:#a0a9b8"></i>已解决</span></div></div>${chart()}<p class="chart-tip" id="chart-tip">北京时间 · 解决率按所选期间创建的工单计算</p></div><div class="panel category-chart"><h2 class="section-title">待回复分类 <span class="muted small">（共 ${count('open')}）</span></h2><div class="category-bars">${groups.map(c=>`<div class="bar-row"><span>${esc(c.name)}</span><div class="bar-track"><div class="bar-fill" style="width:${c.n/largest*100}%"></div></div><b>${c.n}</b></div>`).join('')}</div><button class="link-btn" data-page="categories">管理分类 ${icon('back','reverse')}</button></div></section>${workTable(true)}`;
  }
  function filteredTickets(){const q=state.query.trim().toLowerCase();return data.tickets.filter(t=>(state.status==='all'||t.status===state.status)&&(state.category==='all'||t.category===state.category)&&(!q||`${t.id} ${t.uid} ${t.subject} ${category(t.category).name}`.toLowerCase().includes(q))).sort((a,b)=>b.wait-a.wait||b.id-a.id);}
  function workTable(short=false){
    const all=filteredTickets(),rows=short?all.slice(0,5):all;
    return `<section class="panel work-panel"><div class="work-heading"><h2 class="work-title">${short?'需要处理的工单':'工单收件箱'}</h2><div class="work-controls"><div class="tabs" role="group" aria-label="工单状态">${['open','active','waiting',...(short?[]:['resolved','all'])].map(s=>`<button class="ticket-tab ${state.status===s?'active':''}" data-status="${s}" aria-pressed="${state.status===s}">${statuses[s]||'全部'} ${s==='all'?data.tickets.length:count(s)}</button>`).join('')}</div>${short?btn('查看全部','all-tickets','','primary'):`<span class="muted small">${all.length} 条</span>`}</div></div>${state.selected.size?`<div class="bulk-bar"><span>已选择 ${state.selected.size} 项</span><label class="sr-only" for="bulk-assignee">批量分配负责人</label><select id="bulk-assignee"><option value="">选择负责人</option>${data.team.map(p=>`<option value="${esc(p.id)}">${esc(p.name)}</option>`).join('')}</select>${btn('分配','bulk-assign')}${btn('标为已解决','bulk-resolve')}</div>`:''}<div class="table-scroll"><table><thead><tr><th class="check-cell"><input type="checkbox" id="select-all" aria-label="选择当前列表全部工单" ${rows.length&&rows.every(t=>state.selected.has(t.id))?'checked':''}></th><th>工单 / 问题</th><th>用户</th><th>分类</th><th>优先级</th><th>负责人</th><th>等待时长</th></tr></thead><tbody>${rows.map((t,i)=>`<tr data-ticket="${t.id}" class="${i===0?'selected':''}" tabindex="0" aria-label="查看工单 ${t.id} ${esc(t.subject)}"><td class="check-cell"><input type="checkbox" data-select="${t.id}" aria-label="选择工单 ${t.id}" ${state.selected.has(t.id)?'checked':''}></td><td class="subject-cell"><div class="subject-text"><span class="ticket-id">#${t.id}</span><b>${esc(t.subject)}</b></div></td><td>用户 ${t.uid}</td><td>${esc(category(t.category).name)}</td><td><span class="tag ${t.priority==='high'?'amber':''}">${t.priority==='high'?'高':'普通'}</span></td><td>${esc(t.assignee||'待分配')}</td><td>${t.status==='resolved'?'—':`${t.wait} 分钟`}</td></tr>`).join('')}</tbody></table>${!rows.length?'<div class="empty">没有符合条件的工单，可调整分类、状态或搜索条件。</div>':''}</div><div class="table-foot">${icon('info')}已按等待时长排序${short&&all.length>5?` · 显示 ${all.length} 条中的前 5 条`:''}</div></section>`;
  }
  function inbox(){return header('工单收件箱','按分类、状态和等待时长处理咨询')+`<div class="inbox-toolbar"><label class="search-field">${icon('search')}<input id="inbox-search" aria-label="搜索工单" value="${esc(state.query)}" placeholder="搜索工单、用户或问题"></label><label class="sr-only" for="admin-category-filter">工单分类</label><select id="admin-category-filter">${options(state.category,true)}</select>${btn('清除筛选','clear-filters')}</div>${workTable()}`;}
  function categories(){return header('分类管理','维护问题分类、默认优先级与响应目标',btn(icon('plus')+'新增分类','new-category','','primary'))+`<section class="panel table-scroll"><table class="manage-table"><thead><tr><th>分类名称</th><th>待处理</th><th>默认优先级</th><th>响应目标</th><th>启用</th><th>排序与管理</th></tr></thead><tbody>${data.categories.map((c,i)=>`<tr><td><span class="category-name"><i class="dot" style="background:${esc(c.color)}"></i>${esc(c.name)}</span></td><td>${data.tickets.filter(t=>t.category===c.id&&t.status!=='resolved').length}</td><td><span class="tag ${c.priority==='high'?'amber':''}">${c.priority==='high'?'高':'普通'}</span></td><td>${c.sla} 分钟</td><td><button class="pill-switch" role="switch" aria-checked="${c.enabled}" aria-label="启用${esc(c.name)}" data-toggle-category="${esc(c.id)}"></button></td><td><div class="category-row-actions"><button class="icon-btn" aria-label="上移${esc(c.name)}" data-move-category="${esc(c.id)}" data-direction="-1" ${i===0?'disabled':''}>${icon('up')}</button><button class="icon-btn" aria-label="下移${esc(c.name)}" data-move-category="${esc(c.id)}" data-direction="1" ${i===data.categories.length-1?'disabled':''}>${icon('down')}</button>${btn('编辑','edit-category',`data-id="${esc(c.id)}"`)}</div></td></tr>`).join('')}</tbody></table></section><p class="category-helper">停用后不再提供新咨询入口，历史工单和分类统计仍会保留。</p>`;}
  const helpItems=[['充值未到账怎么办','请先查询原充值订单，不要重复付款。若已付款但仍未到账，请在咨询中提供订单编号、发生时间和付款记录截图。请遮挡与本次问题无关的个人信息，勿发送密码、密钥或验证码。'],['如何查询付款记录','在钱包的充值记录中查找对应订单。通过原订单页面检查付款结果；如有异常，请保留订单编号和付款凭证，再联系在线客服。'],['接口调用如何排查','先核对客户端使用的接口地址、模型名称和错误码。请提供请求编号和发生时间，不要发送完整 API Key。客服可依据这些信息进一步排查。'],['如何设置共享成员预算','成员预算是消费上限，不独占池余额。请在共享额度页面的成员管理中设置预算，具体权益与实际扣费以账户和订单规则为准。']];
  function knowledge(){return header('知识库','整理常见问题，帮助用户先找到答案',btn('查看用户界面','user-view'))+`<section class="panel text-list">${helpItems.map(([title,body],i)=>`<article class="text-item"><div class="between"><h3>${title}</h3><span class="tag">${i<2?'充值与账单':i===2?'模型与 API':'套餐与额度'}</span></div><p>${body}</p><div class="row">${btn('查看文章','help',`data-id="${i}"`)}</div></article>`).join('')}</section>`;}
  function replies(){return header('快捷回复','选择模板后仍可编辑，避免重复输入',btn(icon('plus')+'新增模板','new-reply','','primary'))+`<section class="panel text-list">${data.replies.map(r=>`<article class="text-item"><div class="between"><h3>${esc(r.title)}</h3>${btn('编辑','edit-reply',`data-id="${r.id}"`)}</div><p>${esc(r.text)}</p></article>`).join('')}</section>`;}
  function team(){return header('团队与分配','负责人用于工单分配；管理权限仍由平台管理员账号控制',btn(icon('plus')+'添加负责人','new-team','','primary'))+`<div class="team-grid">${[...data.team.map(p=>p.name),'待分配'].map(name=>{const rows=data.tickets.filter(t=>t.status!=='resolved'&&(t.assignee||'待分配')===name);return `<section class="panel team-person">${avatar(true)}<h3>${esc(name)}</h3><p>待处理 ${rows.length} 条 · 高优先级 ${rows.filter(t=>t.priority==='high').length} 条</p><div class="row" style="margin-top:17px">${btn('分配工单','all-tickets')}</div></section>`;}).join('')}</div>`;}
  function settings(){return header('服务设置','配置用户可见的服务说明与响应目标')+`<form id="settings-form" class="panel settings-form"><div class="form-group"><label for="greeting">用户欢迎语</label><textarea id="greeting" maxlength="300">${esc(data.settings.greeting)}</textarea></div><div class="form-grid"><div class="form-group"><label for="hours">服务时间</label><input class="field" id="hours" value="${esc(data.settings.hours)}" required maxlength="100"></div><div class="form-group"><label for="sla">默认响应目标（分钟）</label><input class="field" id="sla" type="number" min="1" max="1440" value="${data.settings.sla}" required></div></div><p class="form-help">保存后同步到用户咨询入口，历史工单和消息保持留存。</p><div class="row" style="margin-top:18px"><button class="btn primary" type="submit">保存设置</button><a class="preview-shortcut" href="support.html">查看用户界面</a></div></form>`;}
  function admin(){
    const names={overview:'服务概览',inbox:'工单收件箱',categories:'分类管理',replies:'快捷回复',knowledge:'知识库',team:'团队与分配',settings:'服务设置'};
    const views={overview,inbox,categories,replies,knowledge,team,settings};
    return `<div class="admin-shell ${state.nav?'nav-open':''}">${sidebar()}<main class="admin-main" id="main"><div class="admin-topbar"><button class="icon-btn mobile-menu" data-action="toggle-nav" aria-label="切换管理导航">${icon('menu')}</button><div class="breadcrumb">客服中心<span>/</span>${names[state.page]}</div><label class="search-field">${icon('search')}<input id="global-search" value="${esc(state.query)}" placeholder="搜索工单、用户或请求编号" aria-label="搜索工单、用户或请求编号"></label><a class="preview-shortcut" href="support.html">用户界面</a><button class="icon-btn plain" data-action="notifications" aria-label="通知">${icon('bell')}</button><span class="demo-badge">实时数据</span></div>${views[state.page]()}</main></div>${state.drawer?drawer(ticket(state.drawer)):''}`;
  }
  function messageHTML(m,t){const agent=m.role==='agent',system=m.role==='system',note=m.role==='note';if(system)return `<div class="day-separator">${esc(m.text)}</div>`;return `<article class="message ${agent||note?'agent':'user'}">${avatar(agent||note)}<div class="message-body">${agent||note?`<div class="message-author">${note?'内部备注':'客服'}</div>`:''}<div class="message-bubble ${note?'internal-note':''}">${esc(m.text)}${(m.files||[]).map(f=>`<button type="button" class="attachment-tile attachment-button" data-action="attachment" data-id="${esc(f.id)}" data-name="${esc(f.name)}">${icon('image')}<span><strong>${esc(f.name)}</strong><span>图片 · ${Math.max(1,Math.round(f.size/1024))} KB · 点击查看</span></span></button>`).join('')}</div><p class="message-time">${esc(m.time)}${m.role==='user'?' · 已送达':''}</p></div></article>`;}
  function messages(t){return `<div class="day-separator">${esc(t.created)}</div>${t.loading?'<div class="empty">正在加载消息…</div>':t.messages.length?t.messages.map(m=>messageHTML(m,t)).join(''):'<div class="empty">此咨询尚无消息。</div>'}`;}
  function composer(t,isAdmin=false){const key=`${isAdmin?'admin':'user'}-${t.id}`;return `<form class="composer" id="${isAdmin?'admin':'user'}-message-form" data-id="${t.id}">${pendingFiles.length&&!isAdmin?`<div class="attachments-pending">${pendingFiles.map(f=>esc(f.name)).join('、')} ${btn('移除','clear-files','','subtle')}</div>`:''}<div class="composer-box"><label class="sr-only" for="${isAdmin?'admin':'user'}-message">${isAdmin?'客服回复':'消息内容'}</label><textarea id="${isAdmin?'admin':'user'}-message" data-draft="${key}" placeholder="${t.status==='resolved'?'此咨询已解决，可新建咨询继续反馈':'补充问题描述或发送消息…'}" maxlength="${data.settings.maxText||2000}" ${t.status==='resolved'?'disabled':''}>${esc(drafts.get(key)||'')}</textarea><div class="composer-footer">${isAdmin?`<label class="sr-only" for="reply-template">插入快捷回复</label><label class="note-toggle"><input id="internal-note" type="checkbox" ${state.internalNote?'checked':''}>内部备注</label><select id="reply-template"><option value="">快捷回复</option>${data.replies.map(r=>`<option value="${r.id}">${esc(r.title)}</option>`).join('')}</select>`:`<label class="btn attach">${icon('attach')}添加图片<input type="file" id="message-images" accept="image/png,image/jpeg,image/webp" multiple hidden ${t.status==='resolved'?'disabled':''}></label><span class="composer-hint">请勿发送密钥或验证码</span>`}<span class="key-hint">Enter 发送</span><button class="btn primary" type="submit" ${t.status==='resolved'?'disabled':''}>发送</button></div></div><p class="composer-error" id="composer-error" role="alert" hidden></p></form>`;}
  function drawer(t){if(!t)return '';return `<div class="drawer-overlay" data-action="close-drawer-background"><section class="drawer" role="dialog" aria-modal="true" aria-labelledby="drawer-title"><header class="drawer-header"><div><h2 id="drawer-title">${esc(t.subject)}</h2><div class="drawer-meta">#${t.id} · 用户 ${t.uid} ${statusTag(t)}</div></div><button class="icon-btn" data-action="close-drawer" aria-label="关闭工单详情">${icon('close')}</button></header><div class="drawer-controls"><label>问题分类<select id="drawer-category">${options(t.category)}</select></label><label>负责人<select id="drawer-assignee">${['',...data.team.map(p=>p.name)].map(n=>`<option value="${esc(n)}" ${n===t.assignee?'selected':''}>${esc(n||'待分配')}</option>`).join('')}</select></label><label>状态<select id="drawer-status">${Object.entries(statuses).map(([k,v])=>`<option value="${k}" ${t.status===k?'selected':''}>${v}</option>`).join('')}</select></label></div><div class="drawer-messages">${messages(t)}</div>${composer(t,true)}</section></div>`;}
  function previewItem(t){return `<button class="ticket-preview ${state.userTicket===t.id?'active':''}" data-user-ticket="${esc(t.id)}" ${state.userTicket===t.id?'aria-current="true"':''}><span class="preview-top"><span>${esc(category(t.category).name)}</span><time>${esc(t.updated)}</time>${t.unread?'<span class="unread-dot"></span>':''}</span><span class="preview-subject"><strong>${esc(t.subject)}</strong>${statusTag(t)}</span><span class="preview-snippet">${esc(t.lastText||'等待客服接入')}</span></button>`;}
  function userList(){const q=state.userQuery.toLowerCase(),items=own().filter(t=>(state.userTab==='resolved'?t.status==='resolved':t.status!=='resolved')&&(state.userCategory==='all'||t.category===state.userCategory)&&`${t.subject} ${t.id} ${category(t.category).name}`.toLowerCase().includes(q));
    return `<aside class="user-sidebar"><div class="user-sidebar-top"><div class="between"><h2>我的咨询</h2>${btn(icon('plus')+'新建咨询','new-ticket','','new-ticket blue-outline')}</div><label class="search-field">${icon('search')}<input id="user-search" placeholder="搜索咨询记录" aria-label="搜索咨询记录" value="${esc(state.userQuery)}"></label><div class="segmented" role="group" aria-label="咨询状态"><button class="segment ${state.userTab==='active'?'active':''}" data-user-tab="active" aria-pressed="${state.userTab==='active'}">进行中 ${own().filter(t=>t.status!=='resolved').length}</button><button class="segment ${state.userTab==='resolved'?'active':''}" data-user-tab="resolved" aria-pressed="${state.userTab==='resolved'}">已解决 ${own().filter(t=>t.status==='resolved').length}</button></div><h3 class="filter-heading">问题分类</h3><div class="category-filter"><label class="sr-only" for="user-category-filter">筛选问题分类</label><select id="user-category-filter">${options(state.userCategory,true)}</select><button class="icon-btn" data-action="clear-user-filter" aria-label="清除咨询筛选">${icon('filter')}</button></div></div><div class="user-tickets">${items.map(t=>previewItem(t)).join('')||'<div class="empty">没有符合条件的咨询。</div>'}${state.userTab==='active'&&!q&&state.userCategory==='all'&&own().some(t=>t.status==='resolved')?'<div class="recent-label">最近已解决</div>'+own().filter(t=>t.status==='resolved').slice(0,1).map(t=>previewItem(t,true)).join(''):''}</div><div class="user-sidebar-bottom"><button class="link-btn" data-action="help" data-id="0">${icon('book')}常见问题</button><p>${icon('info')}咨询记录会保存在你的账户中</p><a href="/console">${icon('back')}返回控制台</a></div></aside>`;
  }
  function inspector(t){return `<aside class="user-inspector" aria-label="咨询信息"><h2 class="inspector-heading">咨询信息</h2><dl class="info-list"><div class="info-pair"><dt>当前状态</dt><dd>${statusTag(t)}</dd></div><div class="info-pair"><dt><label for="ticket-category">问题分类</label></dt><dd><select id="ticket-category">${options(t.category)}</select></dd></div><div class="info-pair"><dt>服务顾问</dt><dd>${esc(t.assignee?'客服'+t.assignee:'待分配')}</dd></div><div class="info-pair"><dt>创建时间</dt><dd>${esc(t.created)}</dd></div><div class="info-pair"><dt>最近回复</dt><dd>${esc(t.updated)}</dd></div></dl><section class="inspector-section"><h2 class="inspector-heading">处理进度</h2><div class="timeline"><div class="timeline-item done"><span class="timeline-dot">${icon('check')}</span>咨询已提交<time>${esc(t.created)}</time></div><div class="timeline-item ${t.status==='resolved'?'done':'active'}"><span class="timeline-dot">${t.status==='resolved'?icon('check'):''}</span>${t.status==='waiting'?'等待你补充':'客服核对中'}<time>${esc(t.updated)}</time></div><div class="timeline-item ${t.status==='resolved'?'done':''}"><span class="timeline-dot">${t.status==='resolved'?icon('check'):''}</span>${t.status==='resolved'?'已确认解决':'等待解决确认'}</div></div></section><section class="inspector-section"><h2 class="inspector-heading">相关帮助</h2>${helpItems.slice(0,2).map(([title],i)=>`<button class="link-btn help-link" data-action="help" data-id="${i}">${title}${icon('chevron')}</button>`).join('')}</section><p class="privacy-note">${icon('shield')}${esc(data.privacyNote||'仅你与客服可查看本次咨询。')}</p></aside>`;}
  function user(){let t=ticket(state.userTicket);if(!t){t=own()[0];state.userTicket=t?.id;}if(!t)state.showList=true;return `<div class="user-shell ${state.showList?'show-ticket-list':''}"><header class="user-header">${brand()}<span class="header-divider"></span><span class="header-title">帮助与支持</span><div class="header-right"><a class="preview-shortcut" href="/console">返回控制台</a><span class="avatar big">${icon('user')}</span><span class="demo-badge">在线客服</span></div></header><div class="user-workspace">${userList()}<main class="conversation" id="main">${t?`<header class="conversation-header"><button class="icon-btn mobile-back" data-action="show-user-list" aria-label="返回咨询列表">${icon('back')}</button><div class="grow"><h1>${esc(t.subject)}</h1><div class="conversation-sub">#${t.id} · ${esc(category(t.category).name)} ${statusTag(t)}</div></div>${btn(t.status==='resolved'?'重新打开':'确认解决',t.status==='resolved'?'reopen-user':'resolve-user',`data-id="${t.id}"`)}</header><div class="info-banner">${icon('info')}<span>${t.status==='resolved'?'本次咨询已解决，历史记录已保留。':t.status==='waiting'?'客服需要更多信息，请在下方补充描述。':'客服正在处理，请勿重复提交或重复付款。'}</span></div><div class="messages" id="messages" role="log" aria-label="咨询沟通记录" aria-live="polite">${messages(t)}</div>${composer(t)}`:'<div class="empty">还没有咨询，点击「新建咨询」开始。</div>'}</main>${t?inspector(t):''}</div></div>`;}
  function render(){
    const active=document.activeElement,focus=active?.id,selection=typeof active?.selectionStart==='number'?[active.selectionStart,active.selectionEnd]:null;
    app.innerHTML=surface==='admin'?admin():user();
    if(focus&&document.getElementById(focus)){const el=document.getElementById(focus);el.focus({preventScroll:true});if(selection&&el.setSelectionRange)try{el.setSelectionRange(...selection);}catch{}}
  }
  function showModal(title,body){lastFocus=document.activeElement;modal.className='modal';modal.innerHTML=`<div class="between"><h2 id="modal-title">${title}</h2><button class="icon-btn plain" data-action="close-modal" aria-label="关闭">${icon('close')}</button></div>${body}`;modal.showModal();}
  function closeModal(){modal.close();lastFocus?.focus?.({preventScroll:true});}
  function categoryForm(id){const c=id?category(id):{name:'',color:'#007aff',priority:'normal',sla:30,enabled:true};showModal(id?'编辑分类':'新增分类',`<p class="modal-description">分类会同步用于咨询入口、工单筛选和看板统计。</p><form id="category-form" data-id="${esc(id||'')}"><div class="form-group"><label for="category-name">分类名称</label><input class="field" id="category-name" required maxlength="20" value="${esc(c.name)}" placeholder="例如：开发者工具"></div><div class="form-grid"><div class="form-group"><label for="category-priority">默认优先级</label><select id="category-priority"><option value="normal" ${c.priority==='normal'?'selected':''}>普通</option><option value="high" ${c.priority==='high'?'selected':''}>高</option></select></div><div class="form-group"><label for="category-sla">响应目标（分钟）</label><input class="field" id="category-sla" type="number" required min="1" max="1440" value="${c.sla}"></div></div><div class="form-group"><label for="category-color">分类标记颜色</label><input class="color-control" id="category-color" type="color" value="${esc(c.color)}"></div><p class="form-error" id="modal-error" role="alert" hidden></p><div class="modal-actions">${btn('取消','close-modal')}<button class="btn primary" type="submit">保存分类</button></div></form>`);}
  function replyForm(id){const r=data.replies.find(r=>r.id===Number(id))||{title:'',text:''};showModal(id?'编辑快捷回复':'新增快捷回复',`<form id="reply-form" data-id="${id||''}"><div class="form-group"><label for="reply-title">模板名称</label><input class="field" id="reply-title" required maxlength="30" value="${esc(r.title)}"></div><div class="form-group"><label for="reply-text">回复内容</label><textarea id="reply-text" required maxlength="300">${esc(r.text)}</textarea></div><div class="modal-actions">${btn('取消','close-modal')}<button class="btn primary" type="submit">保存模板</button></div></form>`);}
  function newTicket(){showModal('开始新的咨询',`<p class="modal-description">${esc(data.settings.greeting)}</p><form id="new-ticket-form"><div class="form-group"><label for="new-category">问题分类</label><select id="new-category">${options(data.categories.find(c=>c.enabled)?.id)}</select></div><div class="form-group"><label for="new-subject">咨询主题</label><input class="field" id="new-subject" required maxlength="60" placeholder="简单描述你遇到的问题"></div><div class="form-group"><label for="new-description">问题描述</label><textarea id="new-description" required maxlength="2000" placeholder="请补充发生时间、错误提示或请求编号。"></textarea><p class="form-help">请勿填写密码、API 密钥或验证码。</p></div><div class="modal-actions">${btn('取消','close-modal')}<button class="btn primary" type="submit">发送咨询</button></div></form>`);}
  function exportCSV(){const rows=filteredTickets(),lines=[['工单编号','主题','用户ID','分类','优先级','负责人','状态','等待分钟'],...rows.map(t=>[t.id,t.subject,t.uid,category(t.category).name,t.priority==='high'?'高':'普通',t.assignee||'待分配',statuses[t.status],t.wait])];const csv='\ufeff'+lines.map(row=>row.map(v=>'"'+String(v).replace(/^[=+@-]/,"'$&").replace(/"/g,'""')+'"').join(',')).join('\r\n');const url=URL.createObjectURL(new Blob([csv],{type:'text/csv;charset=utf-8'})),a=document.createElement('a');a.href=url;a.download='OmniAPI-客服工单.csv';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);notify('已导出当前筛选的工单');}
  function nowLabel(){const d=new Date();return `${String(d.getHours()).padStart(2,'0')}:${String(d.getMinutes()).padStart(2,'0')}`;}
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
