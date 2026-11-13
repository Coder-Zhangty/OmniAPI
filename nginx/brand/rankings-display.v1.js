/* Public, explicitly labelled display index. Original APIs are never intercepted. */
(() => {
  'use strict';
  if (window.__omniRankingsDisplay) return;
  window.__omniRankingsDisplay = true;
  /** @typedef {'today'|'week'|'month'|'year'|'all'} RankingPeriod */
  const PERIODS = {today:'近24小时', week:'近7天', month:'近30天', year:'近一年', all:'全部'};
  const BASE = '/brand/rankings/';
  const COLORS = ['#1765ff','#a7d2ff','#92e3f4','#b4e6cc','#ffb33f','#20b5e5','#a68af7','#f487a4','#7dc4c1','#c9bb84','#c5cad3'];
  const VENDORS = {'OpenAI':['O','#42877a'],'Anthropic':['A','#cb8267'],'Google':['G','#4c85e8'],'DeepSeek':['D','#5977ee'],'xAI':['X','#737d8b'],'Moonshot':['K','#737d8b'],'智谱':['Z','#5e6ae8']};
  let root, native, tip, bundle, busy = false, queued = false, mounted = false, lastError = false;
  let selected = periodFromURL(), lastRefresh = 0, generation = 0;
  const onPage = () => /^\/rankings\/?$/.test(location.pathname);
  function periodFromURL() { const p = new URL(location.href).searchParams.get('period'); return Object.hasOwn(PERIODS,p) ? p : 'week'; }
  function el(tag, cls, text) { const n=document.createElement(tag); if(cls)n.className=cls; if(text!==undefined)n.textContent=String(text); return n; }
  function format(value) {
    if (!Number.isFinite(value) || value <= 0) return '0';
    for(const [unit, size] of [['T',1e12],['B',1e9],['M',1e6],['K',1e3]]) {
      if(value >= size) return (value / size).toLocaleString('en-US',{maximumFractionDigits:value/size>=100?0: value/size>=10?1:2})+unit;
    }
    return value.toLocaleString('en-US',{maximumFractionDigits:0});
  }
  function percent(value) { return value>0&&value<.0001?'<0.01%':(value*100).toLocaleString('zh-CN',{maximumFractionDigits:2})+'%'; }
  function dateText(iso, hours=false) {
    return new Intl.DateTimeFormat('sv-SE',{timeZone:'Asia/Shanghai',year:'numeric',month:'2-digit',day:'2-digit',...(hours?{hour:'2-digit',minute:'2-digit'}:{})}).format(new Date(iso));
  }
  function color(vendor) { return (VENDORS[vendor] || ['?', '#8e9bad'])[1]; }
  function logo(vendor) {
    const n=el('span','rk-logo',(VENDORS[vendor] || [vendor.slice(0,1), '#8e9bad'])[0]);
    n.style.setProperty('--rk-vendor',color(vendor)); n.setAttribute('aria-hidden','true'); return n;
  }
  function grow(row, isMover=false) {
    const n=el('span','rk-growth');
    if(selected==='all' || !bundle?.snapshots[selected].window.comparison_available) { n.textContent='—';n.dataset.neutral='true'; return n; }
    if(isMover) { n.textContent=(row.rank_delta>0?'↑':'↓')+Math.abs(row.rank_delta)+' 位'; n.dataset.negative=String(row.rank_delta<0);return n; }
    if(!row.previous_rank && row.model_name) { n.textContent='新上榜';n.dataset.neutral='true';return n; }
    const value=row.growth_pct;
    if(!Number.isFinite(value)) {n.textContent='—';n.dataset.neutral='true';return n;}
    n.textContent=(value<0?'↓':'↑')+(Math.abs(value)<1000?Math.abs(value).toLocaleString('zh-CN',{maximumFractionDigits:1}):format(Math.abs(value)))+'%';n.dataset.negative=String(value<0);n.title='与前一等长区间相比：'+value.toLocaleString('zh-CN',{maximumFractionDigits:2})+'%';return n;
  }
  function modelRow(row, mover=false) {
    const n=el('div','rk-row');n.append(el('span','rk-rank',String(row.rank||row.current_rank)+'.'),logo(row.vendor));
    const entity=el('div','rk-entity');
    // Historical version labels do not imply a currently purchasable model.
    const name=el('span','rk-name',row.model_name);name.title=row.model_name;
    entity.append(name,el('div','rk-vendor','by '+row.vendor));
    const value=el('div','rk-value');
    if(!mover) { const amount=el('strong','',format(row.total_tokens));amount.title=row.total_tokens.toLocaleString('zh-CN')+' 展示指数';value.append(amount,el('small','','指数')); }
    value.append(grow(row,mover));n.append(entity,value);return n;
  }
  function cardHeader(title, subtitle, total) {
    const header=el('header','rk-card-head'), left=el('div');
    left.append(el('h2','',title),el('p','rk-sub',subtitle));header.append(left);
    if(total!==undefined) { const right=el('div','rk-total');const value=el('strong','',format(total));value.title=total.toLocaleString('zh-CN');right.append(value,el('small','','展示指数 · 全部模型'));header.append(right); }
    return header;
  }
  function makeSVG(tag, attributes={}, text) {
    const n=document.createElementNS('http://www.w3.org/2000/svg',tag);
    for(const [key,value] of Object.entries(attributes))n.setAttribute(key,String(value));
    if(text!==undefined)n.textContent=String(text);return n;
  }
  function closeTip() { if(tip)tip.hidden=true; }
  function showTip(event, bucket, points, vendorMode, widthSeconds, snapshot) {
    tip.replaceChildren();tip.hidden=false;
    const beginning=Math.max(Date.parse(bucket),Date.parse(snapshot.window.start));
    const ending=Math.min(Date.parse(bucket)+widthSeconds*1000,Date.parse(snapshot.window.end));
    const title=el('div','rk-tip-title',dateText(beginning,true)+' — '+dateText(ending,true)+'（北京时间）');
    const close=el('button','rk-tip-close','×');close.type='button';close.setAttribute('aria-label','关闭详情');close.addEventListener('click',closeTip);
    tip.append(title,close);
    const total=points.reduce((s,p)=>s+p.tokens,0);
    const sum=el('div','rk-tip-line');sum.append(el('strong','','合计'),el('strong','',format(total)+' 指数'));tip.append(sum);
    points.filter(p=>p.tokens>0).sort((a,b)=>b.tokens-a.tokens).forEach(p=>{
      const row=el('div','rk-tip-line');row.append(el('span','',vendorMode?p.vendor:p.model),el('span','',format(p.tokens)+(vendorMode?' · '+percent(p.share):'')));tip.append(row);
    });
    if(!total)tip.append(el('p','','此区间无用量'));
    const box=event.currentTarget.getBoundingClientRect();
    const x=event.clientX||box.left+box.width/2, y=event.clientY||box.top;
    const bounds=tip.getBoundingClientRect();
    tip.style.left=Math.max(12,Math.min(innerWidth-bounds.width-12,x+12))+'px';
    tip.style.top=Math.max(12,Math.min(innerHeight-bounds.height-12,y+12))+'px';
  }
  function chart(snapshot, vendorMode=false) {
    const wrap=el('div','rk-chart'), history=vendorMode?snapshot.vendor_share_history:snapshot.models_history;
    const points=history.points, groups=new Map();
    for(const p of points) {if(!groups.has(p.ts))groups.set(p.ts,[]);groups.get(p.ts).push(p);}
    if(!points.length) {wrap.append(el('div','rk-empty','此区间暂无数据'));return wrap;}
    const buckets=[...groups.keys()].sort(), totals=buckets.map(ts=>groups.get(ts).reduce((s,p)=>s+p.tokens,0));
    const mobile=innerWidth<640, W=mobile?620:1120, H=mobile?350:330, L=60,R=12,T=20,B=40;
    const width=W-L-R,height=H-T-B,max=vendorMode?1:Math.max(1,...totals)*1.08;
    const svg=makeSVG('svg',{viewBox:`0 0 ${W} ${H}`,role:'img','aria-label':vendorMode?'供应商展示指数占比趋势':'模型展示指数趋势'});
    svg.append(makeSVG('title',{},vendorMode?'各时段供应商展示指数占比':'各时段模型展示指数，包含其他模型'));
    for(let i=0;i<=4;i++) {const y=T+height-height*i/4;svg.append(makeSVG('line',{x1:L,y1:y,x2:W-R,y2:y,class:'rk-grid'}),makeSVG('text',{x:L-8,y:y+4,'text-anchor':'end',class:'rk-axis'},vendorMode?`${i*25}%`:format(max*i/4)));}
    const step=width/buckets.length;
    const series=(vendorMode?history.vendors:history.models).map(p=>p.name);
    // Vendor composition uses the existing area-chart form; invisible columns
    // provide the same pointer, touch and keyboard details as the model bars.
    if(vendorMode) {
      const bottom=Array(buckets.length).fill(0);
      series.forEach((name,index)=>{
        const top=bottom.map((value,i)=>value+(groups.get(buckets[i]).find(p=>p.vendor===name)?.share||0));
        const poly=top.map((value,i)=>`${L+(i+.5)*step},${T+height-value*height}`)
          .concat(bottom.map((value,i)=>`${L+(i+.5)*step},${T+height-value*height}`).reverse()).join(' ');
        svg.append(makeSVG('polygon',{points:poly,fill:COLORS[index%COLORS.length],opacity:.85}));
        top.forEach((v,i)=>bottom[i]=v);
      });
    }
    const hitGroups=[];
    const widthSeconds=selected==='today'?3600:(selected==='week'||selected==='month'?86400:604800);
    buckets.forEach((ts,index)=>{
      const g=makeSVG('g',{role:'button',tabindex:0,'aria-label':dateText(ts,selected==='today')+'，'+format(totals[index])+' 展示指数'});
      if(!vendorMode) {
        let y=T+height;
        for(const p of groups.get(ts)) {const h=p.tokens/max*height; y-=h; const c=series.indexOf(p.model);g.append(makeSVG('rect',{x:L+index*step+step*.08,y,width:step*.84,height:h,fill:COLORS[c%COLORS.length]}));}
      }
      g.append(makeSVG('rect',{x:L+index*step,y:T,width:step,height,fill:'transparent',class:'rk-hit'}));
      for(const name of ['pointerenter','pointermove','click','focus'])g.addEventListener(name,e=>showTip(e,ts,groups.get(ts),vendorMode,widthSeconds,snapshot));
      g.addEventListener('keydown',e=>{if(e.key==='Escape'){closeTip();return;}if(['ArrowRight','ArrowLeft'].includes(e.key)){e.preventDefault();hitGroups[(index+(e.key==='ArrowRight'?1:-1)+buckets.length)%buckets.length]?.focus();}if(e.key==='Enter'||e.key===' '){e.preventDefault();showTip(e,ts,groups.get(ts),vendorMode,widthSeconds,snapshot);}});
      g.addEventListener('pointerleave',e=>{if(e.pointerType==='mouse')closeTip();});
      g.addEventListener('blur',closeTip);hitGroups.push(g);svg.append(g);
      const every=Math.max(1,Math.ceil(buckets.length/(mobile?4:7)));
      if(index%every===0 || index===buckets.length-1 && buckets.length%every>1) {
        const label=selected==='today'?dateText(ts,true).slice(11):dateText(ts).slice(5);
        svg.append(makeSVG('text',{x:L+(index+.5)*step,y:H-15,'text-anchor':'middle',class:'rk-axis'},label));
      }
    });
    wrap.append(svg);return wrap;
  }
  function render() {
    if(!root||!onPage())return;
    closeTip();root.replaceChildren();
    const heading=el('div','rk-heading');heading.append(el('h1','','排行榜'),el('span','rk-badge','展示指数'));root.append(heading);
    const snapshot=bundle?.snapshots[selected];
    let disclosure='展示指数按实际 Token 用量 ×200 计算，不代表真实调用量。';
    if(snapshot) {
      const end=new Date(Date.parse(snapshot.display.simulated_end_exclusive+'T00:00:00+08:00')-86400000);
      disclosure+=' '+snapshot.display.simulated_start+' 至 '+dateText(end)+' 为模拟历史。';
    }
    root.append(el('p','rk-disclosure',disclosure));
    const status=el('p','rk-status');status.setAttribute('role','status');
    status.textContent=bundle?(lastError?'更新暂缓，继续显示上一份完整快照 · ':'')+'更新于 '+dateText(bundle.manifest.generated_at,true)+' · 北京时间 · 每5分钟更新':'正在加载展示指数…';root.append(status);
    const tabs=el('div','rk-tabs');tabs.setAttribute('role','tablist');tabs.setAttribute('aria-label','统计时间范围');
    for(const [period,label] of Object.entries(PERIODS)) {
      const b=el('button','rk-tab',label);b.type='button';b.id='rk-tab-'+period;b.setAttribute('role','tab');b.setAttribute('aria-selected',String(period===selected));b.tabIndex=period===selected?0:-1;b.setAttribute('aria-controls','rk-panel');
      b.addEventListener('click',()=>select(period));
      b.addEventListener('keydown',e=>{const keys=Object.keys(PERIODS),i=keys.indexOf(period);let next;if(e.key==='ArrowRight')next=keys[(i+1)%keys.length];if(e.key==='ArrowLeft')next=keys[(i-1+keys.length)%keys.length];if(e.key==='Home')next=keys[0];if(e.key==='End')next=keys.at(-1);if(next){e.preventDefault();select(next);document.getElementById('rk-tab-'+next)?.focus();}});tabs.append(b);
    }root.append(tabs);
    const panel=el('div');panel.id='rk-panel';panel.setAttribute('role','tabpanel');panel.setAttribute('aria-labelledby','rk-tab-'+selected);root.append(panel);
    if(!snapshot) {const state=el('div','rk-card rk-error',lastError?'展示指数暂时无法加载，请稍后重试。':'正在加载完整快照…');if(lastError){const retry=el('button','rk-retry','重新加载');retry.addEventListener('click',()=>refresh(true));state.append(el('br'),retry);}panel.append(state);return;}
    const range=dateText(snapshot.window.start,true)+' — '+dateText(snapshot.window.end,true)+' · 北京时间';
    const models=el('section','rk-card');models.append(cardHeader('▥ 热门模型',range,snapshot.total_tokens),chart(snapshot));
    const leaderboard=el('div','rk-leaderboard');leaderboard.append(el('h3','','♜ LLM 排行榜'),el('p','rk-sub','展示前20名 · 合计覆盖全部 '+snapshot.all_models_count+' 个模型'));
    const rows=el('div','rk-rows');snapshot.models.forEach(r=>rows.append(modelRow(r)));if(!snapshot.models.length)rows.append(el('p','rk-empty','此区间暂无数据'));leaderboard.append(rows);models.append(leaderboard);panel.append(models);
    const vendors=el('section','rk-card');vendors.append(cardHeader('◉ 供应商占比','按同一展示指数汇总计算'),chart(snapshot,true));
    const vendorRows=el('div','rk-vendor-rows');snapshot.vendors.forEach(v=>{
      const row=el('div','rk-row');row.append(el('span','rk-rank',v.rank+'.'),logo(v.vendor));const entity=el('div','rk-entity');entity.append(el('strong','rk-name',v.vendor));
      const track=el('div','rk-share-track'),bar=el('span');bar.style.width=(v.share*100)+'%';bar.style.setProperty('--rk-vendor',color(v.vendor));track.append(bar);entity.append(track);
      const value=el('div','rk-value');value.append(el('strong','',percent(v.share)),el('div','rk-sub',format(v.total_tokens)+' 指数'));row.append(entity,value);vendorRows.append(row);
    });vendors.append(vendorRows);panel.append(vendors);
    const pulse=el('div','rk-pulse');for(const [key,title] of [['top_movers','↑ 上升榜'],['top_droppers','↓ 下降榜']]) {const box=el('section','rk-card');box.append(el('h3','',title));if(!snapshot[key].length)box.append(el('p','rk-empty',selected==='all'?'全部区间不计算环比升降':'此区间暂无名次变动'));snapshot[key].forEach(r=>box.append(modelRow(r,true)));pulse.append(box);}panel.append(pulse);
    panel.append(el('p','rk-source','K / M / B / T 分别表示千 / 百万 / 十亿 / 万亿展示指数。占比与环比从同一份快照计算；环比对比前一等长区间。“全部”从2025-10-05开始。'));
  }
  function select(period) {
    selected=period;const url=new URL(location.href);url.searchParams.set('period',period);history.replaceState(history.state,'',url);render();
  }
  async function fetchJSON(path) {const response=await fetch(BASE+path,{credentials:'omit',cache:'no-store',signal:AbortSignal.timeout(20000)});if(!response.ok)throw Error('snapshot unavailable');const text=await response.text();return {text,data:JSON.parse(text)};}
  async function refresh(force=false) {
    if(!onPage() || busy || !force && Date.now()-lastRefresh<60000)return;
    busy=true;const id=generation;
    try {
      const {data:manifest}=await fetchJSON('manifest.json');
      if(manifest.schema!==1 || !/^[A-Za-z0-9-]{10,80}$/.test(manifest.version))throw Error('invalid manifest');
      if(bundle?.manifest.version===manifest.version) {lastError=false;lastRefresh=Date.now();if(id===generation)render();return;}
      const snapshots={};
      await Promise.all(Object.keys(PERIODS).map(async period=>{
        const entry=manifest.periods[period],expected=`versions/${manifest.version}/${period}.json`;
        if(entry.path!==expected || !/^[a-f0-9]{64}$/.test(entry.sha256))throw Error('invalid entry');
        const {text,data}=await fetchJSON(entry.path);
        const bytes=await crypto.subtle.digest('SHA-256',new TextEncoder().encode(text));
        const hash=[...new Uint8Array(bytes)].map(v=>v.toString(16).padStart(2,'0')).join('');
        if(hash!==entry.sha256 || !data.success || data.version!==manifest.version || data.data.period!==period || data.data.display.scale!==200 || data.data.display.label!=='展示指数' || !Number.isSafeInteger(data.data.total_tokens))throw Error('inconsistent snapshot');
        snapshots[period]=data.data;
      }));
      bundle={manifest,snapshots};lastError=false;lastRefresh=Date.now();
    } catch (_) {lastError=true;lastRefresh=Date.now();}
    finally {busy=false;if(root?.isConnected&&onPage())render();}
  }
  function findNative() {
    for(const h of document.querySelectorAll('h1')) {
      if(root?.contains(h) || !/^(排行榜|Rankings)$/i.test(h.textContent.trim()))continue;
      let n=h;
      while(n && n!==document.body) {if(n.classList.contains('max-w-[1280px]'))return n.parentElement;n=n.parentElement;}
    }
    return null;
  }
  function reconcile() {
    if(!onPage()) {
      if(mounted){root?.remove();tip?.remove();native?.removeAttribute('data-omni-rankings-hidden');root=undefined;native=undefined;tip=undefined;mounted=false;generation++;}
      return;
    }
    if(!root?.isConnected) {
      const candidate=findNative();if(!candidate)return;
      tip?.remove();
      native=candidate;native.setAttribute('data-omni-rankings-native','');
      root=el('main');root.id='omni-rankings-display';root.setAttribute('aria-label','排行榜展示指数');
      native.before(root);tip=el('aside');tip.id='omni-rankings-tooltip';tip.setAttribute('aria-label','图表详情');tip.hidden=true;document.body.append(tip);
      native.setAttribute('data-omni-rankings-hidden','');mounted=true;generation++;selected=periodFromURL();render();refresh(true);
    } else if(periodFromURL()!==selected) {selected=periodFromURL();render();}
  }
  function schedule(records) {
    if(records && records.every(r=>root?.contains(r.target)||tip?.contains(r.target)))return;
    if(queued)return;queued=true;requestAnimationFrame(()=>{queued=false;reconcile();});
  }
  const css=document.createElement('link');css.rel='stylesheet';css.href='/brand/rankings-display.v1.css';document.head.append(css);
  new MutationObserver(schedule).observe(document.documentElement,{childList:true,subtree:true});
  window.addEventListener('popstate',()=>{reconcile();});
  window.addEventListener('resize',()=>{if(onPage()&&bundle)render();});
  document.addEventListener('keydown',e=>{if(e.key==='Escape')closeTip();});
  document.addEventListener('pointerdown',e=>{if(tip&&!tip.hidden&&!tip.contains(e.target)&&!e.target.closest?.('.rk-chart'))closeTip();});
  document.addEventListener('visibilitychange',()=>{if(!document.hidden)refresh();});
  setInterval(()=>refresh(true),300000);
  reconcile();
})();
