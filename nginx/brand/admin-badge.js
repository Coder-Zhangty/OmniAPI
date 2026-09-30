// 注入到 New API SPA 的小角标：有用户提交充值申请时，在控制台提醒经营者。
//
// 经营者已经登录控制台了，这里不再要求第二遍密码：先看有没有服务端会话 cookie，
// 没有就用控制台的 refresh cookie 换一个短时 access_token，交给充值服务验证是不是
// 管理员（role >= 10）。普通用户拿不到 token，或者角色不够，角标完全不显示，
// 也不会泄露任何信息。
(function(){
  'use strict';
  var LINK='/brand/admin.html';
  var ID='omniapi-topup-notice';
  var TIMER=null;

  function build(){
    var a=document.createElement('a');
    a.id=ID;
    a.href=LINK;
    a.title='打开充值核验页面';
    a.style.cssText='position:fixed;right:18px;bottom:18px;z-index:2147483000;display:none;'
      +'align-items:center;gap:8px;padding:11px 15px;border-radius:999px;border:1px solid transparent;'
      +'font-size:14px;line-height:1;text-decoration:none;font-family:inherit;'
      +'box-shadow:0 6px 20px rgba(0,0,0,.18)';
    document.body.appendChild(a);
    // 挡住内容时随手拖走，位置自己记住。共用模块可能还没加载（老缓存），
    // 没有就退化成「不能拖」，绝不让角标因此不出现。
    if(window.OmniFloat) window.OmniFloat.attach(a,{storageKey:'topup-badge'});
    return a;
  }

  function hide(){
    var node=document.getElementById(ID);
    if(node) node.style.display='none';
    if(TIMER){clearInterval(TIMER);TIMER=null;}
  }

  // 有待核验时用醒目的琥珀色；没有时也留一个浅色状态，否则经营者无法判断
  // 「没提示」到底是没有新申请，还是这个功能坏了。
  function render(count){
    var node=document.getElementById(ID)||build();
    node.replaceChildren();
    if(count>0){
      var dot=document.createElement('span');
      dot.style.cssText='display:inline-block;width:8px;height:8px;border-radius:50%;background:#e0a11b;flex:0 0 auto';
      var label=document.createElement('span');
      label.textContent=count+' 笔待核验充值';
      var arrow=document.createElement('span');
      arrow.textContent='→';
      node.append(dot,label,arrow);
      node.style.borderColor='#d9a441';
      node.style.background='#fff8e6';
      node.style.color='#7a5600';
    }else{
      var idle=document.createElement('span');
      idle.textContent='充值核验 · 暂无待处理';
      node.append(idle);
      node.style.borderColor='rgba(120,140,136,.35)';
      node.style.background='rgba(255,255,255,.88)';
      node.style.color='#5d706d';
    }
    node.style.display='inline-flex';
  }

  function poll(){
    fetch('/brand/admin/api/summary',{cache:'no-store',credentials:'same-origin'})
      .then(function(response){
        if(!response.ok){hide();return null;}   // 会话过期：收起角标，不显示误导性的 0
        return response.json();
      })
      .then(function(payload){
        if(!payload) return;
        render((payload.counts&&payload.counts.pending)||0);
      })
      .catch(function(){});
  }

  function consoleToken(){
    return fetch('/api/user/auth/refresh',{method:'POST',cache:'no-store',credentials:'same-origin'})
      .then(function(response){return response.ok?response.json():null;})
      .then(function(payload){
        var data=(payload&&payload.data)||{};
        var user=data.user||{};
        return {token:data.access_token||'',role:Number(user.role||0)};
      })
      .catch(function(){return {token:'',role:0};});
  }

  function bootstrap(){
    return consoleToken().then(function(got){
      if(!got.token||got.role<10) return false;
      return fetch('/brand/admin/api/session',{method:'POST',cache:'no-store',
        credentials:'same-origin',headers:{'Content-Type':'application/json'},
        body:JSON.stringify({access_token:got.token})})
        .then(function(response){return response.ok;})
        .catch(function(){return false;});
    });
  }

  function start(){
    fetch('/brand/admin/api/session',{cache:'no-store',credentials:'same-origin'})
      .then(function(response){return response.ok?response.json():null;})
      .then(function(payload){
        if(payload&&payload.authenticated) return true;
        return bootstrap();
      })
      .then(function(ok){
        if(!ok) return;
        poll();
        TIMER=setInterval(function(){
          if(document.visibilityState==='visible') poll();
        },60000);
      })
      .catch(function(){});
  }

  // 登录/注册页上不出现任何管理入口：那里本来就不该有「充值核验」这种通往管钱界面的按钮。
  // 判断复用 auth-enhance.js 暴露的同一个函数，路径列表不在这里备份第二份。
  function isAuthPage(){
    return !!(window.OmniAuth&&window.OmniAuth.isAuthPage&&window.OmniAuth.isAuthPage());
  }

  function boot(){if(isAuthPage())return;start();}

  if(document.readyState==='loading') document.addEventListener('DOMContentLoaded',boot);
  else boot();
})();
