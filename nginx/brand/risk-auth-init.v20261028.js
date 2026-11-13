/* OmniAPI risk notice and scoped Turnstile metadata; credentials stay opaque. */
(()=>{'use strict';
 function wrap(ts){if(!ts||ts.__omniRiskAction)return;try{let render=ts.render;Object.defineProperty(ts,'render',{configurable:true,get(){return typeof render==='function'?function(target,options,...args){return render.call(ts,target,{...options,action:'omni_auth'},...args)}:render},set(fn){render=fn}});Object.defineProperty(ts,'__omniRiskAction',{value:true})}catch{}}
 // Do not create window.turnstile before the vendor: its duplicate-load guard
 // uses property existence, including an accessor that currently returns undefined.
 wrap(window.turnstile);
 if(!window.__omniTurnstileLoaderHook){window.__omniTurnstileLoaderHook=true;const append=Node.prototype.appendChild;Node.prototype.appendChild=function(node){if(node?.tagName==='SCRIPT'&&/^https:\/\/challenges\.cloudflare\.com\/turnstile\//.test(node.src||'')){const loaded=node.onload;node.onload=function(event){wrap(window.turnstile);if(typeof loaded==='function')return loaded.call(this,event)};node.addEventListener('load',()=>wrap(window.turnstile),{once:true})}return append.call(this,node)}}
})();
