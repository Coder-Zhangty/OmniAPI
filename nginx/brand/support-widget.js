// 注入到 New API SPA 的客服入口。
//
// 做两件事：
//   1. 给所有人一个「在线客服」按钮，指向 /brand/support.html（用户对话页）；
//   2. 如果当前控制台登录态是管理员，再多给一个「客服工作台」按钮，指向
//      /brand/support-admin.html —— 经营者以前只能自己记住那个地址，这就是他找不到
//      「消息在哪看」的原因。
//
// 判断是不是管理员沿用 admin-badge.js 那一套：用控制台的 refresh cookie 换 access_token，
// 看 role >= 10。普通用户拿不到令牌或角色不够，第二个按钮根本不出现，也不泄露信息。
//
// 服务端关掉客服时（config/support.json 的 enabled=false）两个按钮都不出现。
(function(){
  'use strict';
  var ID='omniapi-support-entry';
  var DESK_ID='omniapi-support-desk-entry';
  var LINK='/brand/support.html';
  var DESK_LINK='/brand/support-admin.html';
  var ADMIN_ROLE=10;

  function base(extra){
    return 'position:fixed;left:18px;z-index:2147483000;padding:10px 14px;'
      +'border-radius:999px;border:1px solid rgba(29,158,117,.45);background:#fff;'
      +'color:#14624a;font-size:14px;line-height:1;text-decoration:none;'
      +'font-family:inherit;box-shadow:0 6px 20px rgba(0,0,0,.16);'+extra;
  }

  function link(id,href,title,text,extra){
    var existing=document.getElementById(id);
    if(existing) return existing;
    var a=document.createElement('a');
    a.id=id;a.href=href;a.target='_blank';a.rel='noopener';
    a.title=title;a.textContent=text;a.style.cssText=base(extra);
    document.body.appendChild(a);
    // 与充值核验角标一样：能拖走、位置记住；共用模块缺席时只是不能拖。
    if(window.OmniFloat) window.OmniFloat.attach(a,{storageKey:id});
    return a;
  }

  function renderUserEntry(){
    link(ID,LINK,'打开在线客服','在线客服','bottom:18px');
  }

  function renderDeskEntry(counts){
    var suffix='';
    if(counts&&Number(counts.needs_human)>0) suffix=' · '+counts.needs_human+' 条待人工';
    else if(counts&&Number(counts.unread_operator)>0) suffix=' · '+counts.unread_operator+' 条未读';
    var node=link(DESK_ID,DESK_LINK,'打开客服工作台（经营者）','客服工作台'+suffix,'bottom:68px');
    node.textContent='客服工作台'+suffix;
    // 有待人工时用琥珀色，跟充值核验那个角标一致，一眼能分辨。
    var urgent=counts&&Number(counts.needs_human)>0;
    node.style.borderColor=urgent?'#d9a441':'rgba(120,140,136,.35)';
    node.style.background=urgent?'#fff8e6':'#fff';
    node.style.color=urgent?'#7a5600':'#14624a';
  }

  // 能不能读队列，本身就是「有没有经营者会话」的判据：读到了就顺手拿计数，
  // 读不到（401）就只给一个不带计数的入口，绝不让按钮消失——消失会让人以为没这功能。
  // 返回值有两种含义，必须分开：
  //   {denied:true}  = 明确没权限（401/403）→ **不出入口**。以前这里无论成败都渲染入口，
  //                    于是「退出登录后工作台气泡还挂着」，看着像谁都能进；
  //   {counts:null}  = 网络抖动/5xx → 仍然出入口，只是不带数字（入口消失比数字不准更糟）。
  function loadDeskCounts(){
    return fetch('/brand/admin/api/support/conversations',{cache:'no-store',credentials:'same-origin'})
      .then(function(response){
        if(response.status===401||response.status===403){forgetRole();return {denied:true};}
        if(!response.ok) return {counts:null};
        return response.json().then(function(payload){return {counts:(payload&&payload.counts)||null};});
      })
      .catch(function(){return {counts:null};});
  }

  // `/api/user/auth/refresh` 是**受关键限流保护**的端点（New API 的 CRITICAL_RATE_LIMIT，
  // 与登录共用同一个桶）。控制台每加载一次页面就会调它，这里再调一次等于翻倍，
  // 刷新十几次就会把桶打满、连带把登录一起锁住（线上真出过：刷新到 429 后登不进去）。
  // 所以一个标签页 10 分钟内只问一次，之后用缓存。
  var ROLE_KEY='omniapi.console_role';
  var ROLE_TTL=10*60*1000;

  function cachedRole(){
    try{
      var raw=window.sessionStorage.getItem(ROLE_KEY);
      var value=raw?JSON.parse(raw):null;
      if(value&&typeof value.role==='number'&&Date.now()-Number(value.at)<ROLE_TTL){
        return Number(value.role);
      }
    }catch(error){}
    return null;
  }

  function rememberRole(role){
    try{
      window.sessionStorage.setItem(ROLE_KEY,JSON.stringify({role:role,at:Date.now()}));
    }catch(error){}
  }

  function forgetRole(){
    try{window.sessionStorage.removeItem(ROLE_KEY);}catch(error){}
  }

  function isOperator(){
    var known=cachedRole();
    if(known!==null) return Promise.resolve(known>=ADMIN_ROLE);
    return fetch('/api/user/auth/refresh',{method:'POST',cache:'no-store',credentials:'same-origin'})
      .then(function(response){return response.ok?response.json():null;})
      .then(function(payload){
        var data=(payload&&payload.data)||{};
        var user=data.user||{};
        var role=Number(user.role||0);
        rememberRole(role);
        return role>=ADMIN_ROLE;
      })
      .catch(function(){return false;});
  }

  // 登录/注册页上不出现任何气泡。
  // 判断**不在这里再写一份**：路径列表只有 auth-enhance.js 那一处（它暴露在
  // window.OmniAuth 上）。两个脚本是同一条 nginx 注入规则加的，一个加载不出来
  // 另一个也不会加载，所以这里不需要兜底副本 —— 备份一份只会日后走样。
  function isAuthPage(){
    return !!(window.OmniAuth&&window.OmniAuth.isAuthPage&&window.OmniAuth.isAuthPage());
  }

  function start(){
    if(isAuthPage())return;
    fetch('/brand/api/support/config',{cache:'no-store'})
      .then(function(response){return response.ok?response.json():null;})
      .then(function(config){
        if(!config||!config.enabled)return;
        renderUserEntry();
        return isOperator().then(function(operator){
          if(!operator)return;
          return loadDeskCounts().then(function(result){
            if(result.denied)return;
            renderDeskEntry(result.counts);
          });
        });
      })
      .catch(function(){});
  }

  if(document.readyState==='loading') document.addEventListener('DOMContentLoaded',start);
  else start();
})();
