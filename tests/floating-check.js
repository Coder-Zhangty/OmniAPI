// 拖动浮动气泡的行为检查（jsdom）。
//
// 为什么值得单独测：这几个气泡都是 `<a>`，拖动一个链接最容易踩的坑是
// 「挪了一下位置，结果被带去别的页面」。阈值、吞掉拖后那次 click、位置记忆、
// 夹在可视区内，这四件事任何一条坏了都只有真人上手才会发现。
//
// 运行：node tests/floating-check.js
const { JSDOM } = require('jsdom');
const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');
let failures = 0;

function check(label, actual, expected) {
  const ok = String(actual) === String(expected);
  if (!ok) failures++;
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${label}: ${JSON.stringify(actual)}${ok ? '' : '  期望 ' + JSON.stringify(expected)}`);
}

function open() {
  const dom = new JSDOM('<!doctype html><html><body></body></html>',
                        { url: 'https://omniapi.biz/console', runScripts: 'outside-only' });
  const w = dom.window;
  w.eval(fs.readFileSync(path.join(ROOT, 'nginx/brand/floating.js'), 'utf8'));
  return w;
}

// jsdom 不做布局：offsetWidth 永远是 0、rect 永远是 0。所以给元素装一个
// 「按 style 算出来」的假 rect —— 这样拖动起点、夹取边界才是被真实检验的。
const WIDTH = 150, HEIGHT = 40;
function pill(w, storageKey) {
  const el = w.document.createElement('a');
  el.href = 'https://omniapi.biz/brand/support.html';
  el.textContent = '在线客服';
  el.style.cssText = 'position:fixed;left:18px;bottom:18px';
  el.getBoundingClientRect = () => {
    const left = parseFloat(el.style.left) || 0;
    const top = parseFloat(el.style.top) || parseFloat(el.style.bottom) || 0;
    return { left, top, width: WIDTH, height: HEIGHT,
             right: left + WIDTH, bottom: top + HEIGHT, x: left, y: top };
  };
  w.document.body.appendChild(el);
  if (storageKey !== undefined) w.OmniFloat.attach(el, { storageKey: storageKey });
  return el;
}

function pointer(w, el, type, x, y) {
  // jsdom 还没有 PointerEvent；MouseEvent 带 clientX/clientY 就够处理器用了。
  const event = new w.MouseEvent(type, { clientX: x, clientY: y, bubbles: true, cancelable: true });
  el.dispatchEvent(event);
  return event;
}

function click(w, el) {
  const event = new w.MouseEvent('click', { bubbles: true, cancelable: true });
  el.dispatchEvent(event);
  return event;
}

console.log('\n[1] 接上之后是可拖的样子');
{
  const w = open();
  const el = pill(w, 'pill-1');
  check('标记为已接（不会重复挂监听）', el.dataset.omniFloat, '1');
  check('光标提示可拖', el.style.cursor, 'grab');
  check('触摸时不顺带滚页面', el.style.touchAction, 'none');
  check('链接自身不再触发浏览器拖拽', el.draggable, false);
  const again = w.OmniFloat.attach(el, { storageKey: 'pill-1' });
  check('重复 attach 返回同一个元素', again === el, true);
  w.close();
}

console.log('\n[2] 轻微的抖动算「点击」，不算「拖动」');
{
  const w = open();
  const el = pill(w, 'pill-2');
  pointer(w, el, 'pointerdown', 30, 740);
  pointer(w, el, 'pointermove', 33, 742);      // 位移 3px，在阈值内
  pointer(w, el, 'pointerup', 33, 742);
  // 气泡自带 left:18px;bottom:18px。要验的是「完全没动过」——一旦 place() 被调用，
  // 定位就会变成 left/top 而 bottom 被清成 auto。
  check('没有挪动位置', el.style.left, '18px');
  check('也没被改成绝对坐标', el.style.bottom, '18px');
  const event = click(w, el);
  check('这一下点击照常生效（没被吞）', event.defaultPrevented, false);
  w.close();
}

console.log('\n[3] 真的拖动会移动，并且吞掉紧随其后的那次点击');
{
  const w = open();
  const el = pill(w, 'pill-3');
  el.style.left = '100px'; el.style.top = '600px';   // 假装已经在某个位置
  pointer(w, el, 'pointerdown', 100, 600);
  pointer(w, el, 'pointermove', 260, 480);           // 位移 > 阈值
  check('跟手移动', el.style.left + ',' + el.style.top, '260px,480px');
  check('清掉了 right/bottom（否则两边同时生效）', el.style.right + '/' + el.style.bottom, 'auto/auto');
  pointer(w, el, 'pointerup', 260, 480);
  const event = click(w, el);
  check('拖完那次点击被吞掉（不会被带去别的页面）', event.defaultPrevented, true);
  const second = click(w, el);
  check('只有紧接的那一次被吞，之后恢复正常', second.defaultPrevented, false);
  w.close();
}

console.log('\n[4] 位置记在本地，刷新后还在');
{
  const w = open();
  const first = pill(w, 'pill-4');
  first.style.left = '50px'; first.style.top = '500px';
  pointer(w, first, 'pointerdown', 50, 500);
  pointer(w, first, 'pointermove', 400, 220);
  pointer(w, first, 'pointerup', 400, 220);
  const saved = JSON.parse(w.localStorage.getItem('omniapi.float.pill-4'));
  check('写进了 localStorage', saved.x + ',' + saved.y, '400,220');

  const again = pill(w, 'pill-4');    // 模拟刷新后重建气泡
  check('重建时还原位置', again.style.left + ',' + again.style.top, '400px,220px');
  w.close();
}

console.log('\n[5] 永远拖不出可视区');
{
  const w = open();
  const el = pill(w, null);           // 不记位置，只看夹取
  pointer(w, el, 'pointerdown', 0, 0);
  pointer(w, el, 'pointermove', 99999, 99999);
  const left = parseFloat(el.style.left), top = parseFloat(el.style.top);
  check('右边被夹住', left, w.innerWidth - WIDTH - w.OmniFloat.MARGIN);
  check('下边被夹住', top, w.innerHeight - HEIGHT - w.OmniFloat.MARGIN);
  pointer(w, el, 'pointermove', -99999, -99999);
  check('左边被夹住', parseFloat(el.style.left), w.OmniFloat.MARGIN);
  check('上边被夹住', parseFloat(el.style.top), w.OmniFloat.MARGIN);
  pointer(w, el, 'pointerup', -99999, -99999);
  w.close();
}

console.log('\n[6] 保存的位置也在可视区内（窗口变小了也不例外）');
{
  const w = open();
  w.localStorage.setItem('omniapi.float.pill-6', JSON.stringify({ x: 5000, y: 5000 }));
  const el = pill(w, 'pill-6');
  check('超界的位置被拉回来', parseFloat(el.style.left) <= w.innerWidth - WIDTH, true);
  check('纵向同理', parseFloat(el.style.top) <= w.innerHeight - HEIGHT, true);
  check('没有跑到负坐标', parseFloat(el.style.left) >= 0 && parseFloat(el.style.top) >= 0, true);
  w.close();
}

console.log('\n[7] 气泡里的表单控件照常工作（「显示币种」下拉框）');
{
  const w = open();
  // 复刻「显示币种」挂件的结构：外层可拖，里面有个 select。
  const wrap = w.document.createElement('div');
  wrap.style.cssText = 'position:fixed;right:22px;bottom:78px';
  const select = w.document.createElement('select');
  select.append(w.document.createElement('option'));
  wrap.appendChild(select);
  wrap.getBoundingClientRect = () => ({ left: 300, top: 400, width: 240, height: 44,
                                        right: 540, bottom: 444, x: 300, y: 400 });
  w.document.body.appendChild(wrap);
  w.OmniFloat.attach(wrap, { storageKey: 'currency' });

  const down = pointer(w, select, 'pointerdown', 320, 420);
  check('在控件上按下不会阻止默认行为（否则下拉打不开）', down.defaultPrevented, false);
  pointer(w, select, 'pointermove', 520, 260);
  check('在控件上拖动不会挪走气泡', wrap.style.left, '');

  pointer(w, wrap, 'pointerdown', 300, 400);
  pointer(w, wrap, 'pointermove', 200, 300);
  check('在气泡空白处仍然可以拖', wrap.style.left + ',' + wrap.style.top, '200px,300px');
  pointer(w, wrap, 'pointerup', 200, 300);
  w.close();
}

console.log('\n[8] 共用模块缺席时挂件照常出现（老缓存也不会白屏）');
{
  const w = open();
  delete w.OmniFloat;
  const js = fs.readFileSync(path.join(ROOT, 'nginx/brand/support-widget.js'), 'utf8');
  w.fetch = async (target) => {
    const href = String(target);
    const json = (status, payload) => ({ ok: status < 400, status, json: async () => payload });
    if (href.startsWith('/brand/api/support/config')) return json(200, { success: true, enabled: true });
    return json(401, { success: false });
  };
  w.eval(js);
  setTimeout(() => {
    check('气泡仍然出现', w.document.getElementById('omniapi-support-entry') !== null, true);
    check('只是没有挂上拖拽', w.document.getElementById('omniapi-support-entry').dataset.omniFloat, undefined);
    console.log(failures === 0 ? '\n全部通过' : `\n有 ${failures} 项失败`);
    process.exit(failures === 0 ? 0 : 1);
  }, 80);
}
