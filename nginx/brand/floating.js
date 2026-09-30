// 让浮动气泡可以拖动。
//
// 控制台左下/右下那几个气泡（在线客服、客服工作台、充值核验）会挡住页面内容，
// 每个人想放的位置也不一样，所以做成一拖就走、并且记住位置。
//
// 用法（放在注入脚本之前先加载）：
//     OmniFloat.attach(element, {storageKey: 'topup-badge'});
//
// 几个刻意的取舍：
//
// - **拖动之后不能触发点击。** 这些气泡都是 `<a>`，挪一下就被带去别的页面是最烦人的
//   体验，所以位移超过阈值后，紧随其后的那次 click 会被吞掉。
// - **阈值 6px。** 手指/鼠标按下时总会有抖动，低于阈值一律当成「点」而不是「拖」。
// - **位置存在 localStorage。** 刷新、切页、SPA 路由变化都保持；存不下（隐私模式）
//   就退化成「本次有效」，不影响拖动本身。
// - **永远夹在可视区内。** 拖到屏幕外就再也点不到了，所以每次落点都夹一次，
//   窗口缩放时也重新夹一次。
// - 用 Pointer Events 一套覆盖鼠标 / 触摸 / 触控笔；移动与松开挂在 `window` 上，
//   这样指针滑出元素也不会丢事件，不需要 setPointerCapture。
(function(){
  'use strict';
  if(window.OmniFloat) return;

  var MARGIN=6;        // 与视口边缘至少留这么多
  var THRESHOLD=6;     // 位移小于这个数就当成点击
  var PREFIX='omniapi.float.';

  function read(key){
    if(!key) return null;
    try{
      var raw=window.localStorage.getItem(PREFIX+key);
      var value=raw?JSON.parse(raw):null;
      return value&&typeof value.x==='number'&&typeof value.y==='number'?value:null;
    }catch(error){return null;}
  }

  function write(key,position){
    if(!key) return;
    try{window.localStorage.setItem(PREFIX+key,JSON.stringify(position));}catch(error){}
  }

  function clamp(value,low,high){
    if(high<low) return low;
    return Math.max(low,Math.min(high,value));
  }

  // offsetWidth 在元素隐藏（display:none）时是 0，所以要退回到 rect。
  function sizeOf(element){
    var rect=element.getBoundingClientRect();
    return {width:element.offsetWidth||rect.width||0,
            height:element.offsetHeight||rect.height||0};
  }

  function place(element,x,y){
    element.style.left=Math.round(x)+'px';
    element.style.top=Math.round(y)+'px';
    // 默认定位用的是 right/bottom，一旦改成绝对坐标必须清掉，否则两边同时生效。
    element.style.right='auto';
    element.style.bottom='auto';
  }

  function attach(element,options){
    if(!element||element.dataset.omniFloat) return element;
    element.dataset.omniFloat='1';
    var key=(options&&options.storageKey)||'';
    var position=read(key);
    var drag=null;
    var swallowClick=false;

    element.style.touchAction='none';    // 触摸拖动时不要顺便滚动页面
    element.style.cursor='grab';
    element.style.userSelect='none';
    // 链接默认可拖动，会和我们的拖动打架。
    element.draggable=false;
    element.addEventListener('dragstart',function(event){event.preventDefault();});

    function settle(x,y){
      var box=sizeOf(element);
      if(box.width&&box.height){
        x=clamp(x,MARGIN,Math.max(MARGIN,window.innerWidth-box.width-MARGIN));
        y=clamp(y,MARGIN,Math.max(MARGIN,window.innerHeight-box.height-MARGIN));
      }
      place(element,x,y);
      return {x:parseFloat(element.style.left)||x,y:parseFloat(element.style.top)||y};
    }

    if(position) settle(position.x,position.y);

    // 气泡里可能有表单控件（「显示币种」那个下拉框就是）。在控件上按下时必须
    // 完全不插手：一旦对 pointerdown 调了 preventDefault，原生下拉就打不开了，
    // 表现是「拖是能拖，但选不了币种」——很难猜到是拖动模块干的。
    var CONTROLS='input,select,textarea,button,option,label,a[href]';
    function onControl(target){
      if(!target||target===element) return false;      // 气泡本身就是链接，得能拖
      if(typeof target.closest!=='function') return false;
      return !!target.closest(CONTROLS);
    }

    function onMove(event){
      if(!drag) return;
      if(drag.pointerId!==null&&event.pointerId!==undefined&&event.pointerId!==drag.pointerId) return;
      var dx=event.clientX-drag.startX;
      var dy=event.clientY-drag.startY;
      if(!drag.moved&&Math.abs(dx)<THRESHOLD&&Math.abs(dy)<THRESHOLD) return;
      drag.moved=true;
      element.style.cursor='grabbing';
      var current=settle(drag.originX+dx,drag.originY+dy);
      position=current;
    }

    function onUp(event){
      if(!drag) return;
      var moved=drag.moved;
      drag=null;
      element.style.cursor='grab';
      window.removeEventListener('pointermove',onMove);
      window.removeEventListener('pointerup',onUp);
      window.removeEventListener('pointercancel',onUp);
      if(moved){
        if(position) write(key,position);
        // 紧随其后会有一次 click；想挪位置的人不该因此被带到别的页面。
        swallowClick=true;
        if(event&&event.cancelable) event.preventDefault();
      }
    }

    element.addEventListener('pointerdown',function(event){
      if(event.button!==undefined&&event.button!==0) return;   // 只认左键
      if(onControl(event.target)) return;                      // 让表单控件自己工作
      var rect=element.getBoundingClientRect();
      drag={pointerId:event.pointerId===undefined?null:event.pointerId,
            startX:event.clientX,startY:event.clientY,
            originX:rect.left,originY:rect.top,moved:false};
      window.addEventListener('pointermove',onMove);
      window.addEventListener('pointerup',onUp);
      window.addEventListener('pointercancel',onUp);
      if(event.cancelable) event.preventDefault();
    });

    element.addEventListener('click',function(event){
      if(!swallowClick) return;
      swallowClick=false;
      event.preventDefault();
      event.stopPropagation();
    },true);

    window.addEventListener('resize',function(){
      if(drag||!position) return;
      var box=sizeOf(element);
      if(!box.width||!box.height) return;      // 隐藏中，量不到就先不动
      position=settle(position.x,position.y);
      write(key,position);
    });

    return element;
  }

  // 抹掉记住的位置，下次回到默认角落。没有界面入口，留给排障用。
  function forget(key){
    try{window.localStorage.removeItem(PREFIX+key);}catch(error){}
  }

  window.OmniFloat={attach:attach,forget:forget,MARGIN:MARGIN,THRESHOLD:THRESHOLD};
})();
