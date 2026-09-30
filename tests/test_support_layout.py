#!/usr/bin/env python3
"""真实浏览器里的界面检查：布局能不能用、气泡能不能拖。

这一层的共同点：**它们都在验 jsdom 验不了的东西**——jsdom 不做布局，也没有
PointerEvent，所以「输入框只有 22px」「拖完还被带去别的页面」这类问题它一律看不见。

为什么要有这个测试：`plans.css` 里有一条给套餐页竖排表单写的全局 `button{width:100%}`
（还有 `textarea{width:100%}`）。客服页会把 `.composer` 设成 flex 行，一旦不显式复位
`width`，「发送」按钮就会吃掉整行，把输入框挤成 **22px 宽的一条缝**——页面看着正常，
但用户一个字也打不进去，而 jsdom 检查（`tests/support-page-check.js`）完全看不出来，
因为 jsdom 不做布局。

所以这里分两层：

1. **静态检查**（任何机器都能跑）：每个用 flex 横排按钮的容器，都必须有一条
   `… button { width: … }` 的复位规则。它能抓住「有人把这条规则删了」。
2. **真实测量**（本机有 Chrome/Edge 时）：把页面放进无头浏览器，读出输入框和按钮的
   实际像素宽度并断言。它抓的是结果——哪怕原因换成别的写法，只要输入框又变窄就会红。

没有浏览器时第 2 层会跳过并说明，不会假装通过。
"""
import json
import os
import pathlib
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
PAGES = {
    'support': 'nginx/brand/support.html',
    'support-admin': 'nginx/brand/support-admin.html',
}
# 这些容器都是 `display:flex` 的一行，里面的 button 必须复位 width。
FLEX_ROW_CONTAINERS = ('.composer', '.row-actions', '.quick')
# 输入框至少要这么宽才算能用（原来被挤成 22px）。
MIN_INPUT_WIDTH = 300
MAX_SEND_WIDTH = 200
MAX_ACTION_BUTTON_WIDTH = 300

BROWSER_CANDIDATES = (
    r'C:\Program Files\Google\Chrome\Application\chrome.exe',
    r'C:\Program Files (x86)\Google\Chrome\Application\chrome.exe',
    r'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe',
    r'C:\Program Files\Microsoft\Edge\Application\msedge.exe',
)


def inline_style(name):
    html = (ROOT / PAGES[name]).read_text(encoding='utf-8')
    match = re.search(r'<style>(.*?)</style>', html, re.S)
    return match.group(1) if match else ''


def css_rules(text):
    """(selector, declarations) for every rule in an inline stylesheet.

    Comments are stripped first: they are prose here and they do contain braces
    (someone writing `button{width:100%}` inside a comment would otherwise
    derail the parse).
    """
    text = re.sub(r'/\*.*?\*/', '', text, flags=re.S)
    return [(selector.strip(), body) for selector, body in
            re.findall(r'([^{}]+)\{([^{}]*)\}', text)]


def find_browser():
    for path in BROWSER_CANDIDATES:
        if os.path.isfile(path):
            return path
    for name in ('google-chrome', 'chromium', 'chromium-browser', 'msedge'):
        found = shutil.which(name)
        if found:
            return found
    return os.environ.get('OMNIAPI_CHROME') or None


def render(browser, html):
    """把一段 HTML 丢进无头浏览器，返回 --dump-dom 的输出。"""
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='support-layout-'))
    target = workdir / 'page.html'
    target.write_text(html, encoding='utf-8')
    profile = workdir / 'profile'
    profile.mkdir()
    try:
        return subprocess.run(
            [browser, '--headless=new', '--disable-gpu', '--no-sandbox',
             '--hide-scrollbars', '--virtual-time-budget=2500',
             '--user-data-dir=%s' % profile,
             '--window-size=1680,1400', '--dump-dom', target.as_uri()],
            capture_output=True, text=True, timeout=120)
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


def marked(text):
    """取出被 MEASURE 标记包住的那一段（脚本源码里不会出现这两个字符串）。"""
    match = (re.search(r'MEASURE&gt;&gt;&gt;(.*?)&lt;&lt;&lt;MEASURE', text, re.S)
             or re.search(r'MEASURE>>>(.*?)<<<MEASURE', text, re.S))
    return match.group(1).strip() if match else None


def measure(browser, page):
    """Render the page in a real browser and read back element widths.

    The chat panel is hidden until you log in, so the probe page unhides it and
    fills in a couple of messages: otherwise there would be nothing to measure.
    The stylesheet is inlined because `file://` cannot resolve `/brand/plans.css`.
    """
    html = (ROOT / PAGES[page]).read_text(encoding='utf-8')
    css = (ROOT / 'nginx/brand/plans.css').read_text(encoding='utf-8')
    style = inline_style(page)
    panel = 'chat-panel' if page == 'support' else 'board'
    html = html.replace('<link rel="stylesheet" href="/brand/plans.css">',
                        '<style>%s</style><style>%s</style>' % (css, style), 1)
    html = re.sub(r'<section class="panel" id="gate" hidden>.*?</section>', '', html, flags=re.S)
    html = html.replace('id="%s" hidden' % panel, 'id="%s"' % panel, 1)
    # The page scripts would call the live API; they are not what is being measured.
    html = re.sub(r'<script src="/brand/[^"]+"[^>]*></script>', '', html)
    probe = """
<pre id="measure"></pre>
<script>
function box(sel){
  var el=document.querySelector(sel);
  if(!el)return sel+' = 不存在';
  var r=el.getBoundingClientRect();
  return sel+' = '+Math.round(r.width)+'x'+Math.round(r.height);
}
var out=[];
['#text','.composer button','.row-actions button','.quick button'].forEach(function(sel){
  var nodes=document.querySelectorAll(sel);
  if(!nodes.length){out.push(sel+' = 不存在');return;}
  for(var i=0;i<nodes.length;i++){
    var r=nodes[i].getBoundingClientRect();
    out.push(sel+'['+i+'] = '+Math.round(r.width)+'x'+Math.round(r.height));
  }
});
// 标记由片段拼出来：否则 --dump-dom 的输出里，脚本源码本身也会包含
// 'MEASURE>>>'，正则会先匹配到源码而不是结果。
var OPEN='MEAS'+'URE>>>', CLOSE='<<<'+'MEAS'+'URE';
document.getElementById('measure').textContent=OPEN+out.join(' | ')+CLOSE;
</script>
"""
    html = html.replace('</body>', probe + '</body>', 1)
    workdir = pathlib.Path(tempfile.mkdtemp(prefix='support-layout-'))
    target = workdir / 'page.html'
    target.write_text(html, encoding='utf-8')
    profile = workdir / 'profile'
    profile.mkdir()
    try:
        result = subprocess.run(
            [browser, '--headless=new', '--disable-gpu', '--no-sandbox',
             '--hide-scrollbars', '--virtual-time-budget=2000',
             # Chrome refuses to start without an explicit profile directory
             # when another instance is already running.
             '--user-data-dir=%s' % profile,
             '--window-size=1680,1400', '--dump-dom', target.as_uri()],
            capture_output=True, text=True, timeout=120)
    finally:
        shutil.rmtree(workdir, ignore_errors=True)
    match = re.search(r'MEASURE&gt;&gt;&gt;(.*?)&lt;&lt;&lt;MEASURE', result.stdout, re.S) \
        or re.search(r'MEASURE>>>(.*?)<<<MEASURE', result.stdout, re.S)
    if not match:
        raise AssertionError('浏览器没有返回测量结果；输出片段：%s'
                             % (result.stdout[-400:] + result.stderr[-400:]))
    boxes = {}
    for item in match.group(1).split('|'):
        name, _, size = item.strip().partition(' = ')
        # The probe appends an index when a selector matches several elements
        # (`#text[0] = …`); the tests want them all under the bare selector.
        name = re.sub(r'\[\d+\]$', '', name)
        if size == '不存在':
            boxes.setdefault(name, [])      # the page simply has no such element
            continue
        width, _, height = size.partition('x')
        boxes.setdefault(name, []).append((int(width), int(height)))
    return boxes


class StaticResetTests(unittest.TestCase):
    """任何机器都能跑的静态检查：flex 行里的按钮必须复位 width。"""

    def test_every_flex_row_resets_button_width(self):
        for page in PAGES:
            html = (ROOT / PAGES[page]).read_text(encoding='utf-8')
            rules = css_rules(inline_style(page))
            for container in FLEX_ROW_CONTAINERS:
                if container not in html:
                    continue                      # that page has no such row
                matching = [body for selector, body in rules
                            if selector == container + ' button']
                with self.subTest(page=page, container=container):
                    self.assertTrue(
                        matching,
                        '%s 的 %s 里有按钮，却没有一条 `%s button { width: … }` 复位规则；'
                        'plans.css 的全局 button{width:100%%} 会把输入框挤没。'
                        % (PAGES[page], container, container))
                    self.assertTrue(
                        any('width' in body for body in matching),
                        '%s 的 `%s button` 规则里没有 width —— 按钮仍会撑满整行。'
                        % (PAGES[page], container))

    def test_the_input_is_not_sized_by_the_global_textarea_rule(self):
        for page in PAGES:
            rules = dict(css_rules(inline_style(page)))
            body = rules.get('.composer textarea', '')
            with self.subTest(page=page):
                self.assertIn('flex', body,
                              '%s 的 .composer textarea 必须显式设 flex/min-width，'
                              '否则 plans.css 的 textarea{width:100%%} 会让它和按钮互相挤。' % page)


class MeasuredLayoutTests(unittest.TestCase):
    """真实浏览器测量。没有 Chrome/Edge 时跳过，并把原因说清楚。"""

    @classmethod
    def setUpClass(cls):
        cls.browser = find_browser()
        if cls.browser is None:
            raise unittest.SkipTest(
                '本机没有 Chrome/Edge，跳过像素测量（第 1 层静态检查仍然有效）')

    def test_the_textarea_is_wide_enough_to_type_in(self):
        for page in PAGES:
            boxes = measure(self.browser, page)
            with self.subTest(page=page):
                self.assertTrue(boxes.get('#text'), '页面上找不到输入框')
                width = boxes['#text'][0][0]
                self.assertGreaterEqual(
                    width, MIN_INPUT_WIDTH,
                    '%s 的输入框只有 %dpx 宽（曾经是 22px）：plans.css 的全局 '
                    'button{width:100%%} 又把整行吃掉了。' % (page, width))

    def test_the_send_button_does_not_take_the_whole_row(self):
        for page in PAGES:
            boxes = measure(self.browser, page)
            with self.subTest(page=page):
                self.assertTrue(boxes.get('.composer button'), '找不到「发送」按钮')
                width = boxes['.composer button'][0][0]
                self.assertLessEqual(width, MAX_SEND_WIDTH,
                                     '%s 的「发送」占了 %dpx，输入框又被挤没了。' % (page, width))

    def test_action_buttons_sit_on_one_row(self):
        for page in PAGES:
            boxes = measure(self.browser, page)
            with self.subTest(page=page):
                for index, (width, _) in enumerate(boxes['.row-actions button']):
                    self.assertLessEqual(
                        width, MAX_ACTION_BUTTON_WIDTH,
                        '%s 第 %d 个 row-actions 按钮宽 %dpx，说明它又单独占了一整行。'
                        % (page, index + 1, width))


DRAG_PAGE = """<!doctype html><meta charset="utf-8">
<style>body{margin:0;height:2000px}</style>
<script>%s</script>
<a id="pill" href="#nowhere" style="position:fixed;left:18px;bottom:18px">在线客服</a>
<pre id="out"></pre>
<script>
var el=document.getElementById('pill');
OmniFloat.attach(el,{storageKey:'probe'});
function fire(type,x,y){
  el.dispatchEvent(new PointerEvent(type,{clientX:x,clientY:y,bubbles:true,
                                          cancelable:true,pointerId:1,button:0}));
}
var before=el.getBoundingClientRect();
fire('pointerdown',before.left+5,before.top+5);
fire('pointermove',before.left+205,before.top-155);
fire('pointerup',before.left+205,before.top-155);
var after=el.getBoundingClientRect();
var saved='unavailable';
try{saved=String(localStorage.getItem('omniapi.float.probe'));}catch(error){}
var click=new MouseEvent('click',{bubbles:true,cancelable:true});
el.dispatchEvent(click);
var OPEN='MEAS'+'URE>>>', CLOSE='<<<'+'MEAS'+'URE';
document.getElementById('out').textContent=OPEN+JSON.stringify({
  dx:Math.round(after.left-before.left), dy:Math.round(after.top-before.top),
  inlineLeft:el.style.left, inlineTop:el.style.top, inlineRight:el.style.right,
  saved:saved, clickPrevented:click.defaultPrevented,
  attached:el.dataset.omniFloat}) + CLOSE;
</script>
"""


class MeasuredDragTests(unittest.TestCase):
    """气泡拖动的真实浏览器验证。

    jsdom 里能验阈值与状态机（`tests/floating-check.js`），但**指针事件本身**、
    真实布局下的夹取、以及 localStorage 在真实页面里能不能写，只有真浏览器说了算。
    """

    @classmethod
    def setUpClass(cls):
        cls.browser = find_browser()
        if cls.browser is None:
            raise unittest.SkipTest(
                '本机没有 Chrome/Edge，跳过拖拽的真实浏览器验证（jsdom 版仍然有效）')

    def setUp(self):
        source = (ROOT / 'nginx/brand/floating.js').read_text(encoding='utf-8')
        result = render(self.browser, DRAG_PAGE % source.replace('</script>', '<\\/script>'))
        raw = marked(result.stdout)
        self.assertIsNotNone(raw, '浏览器没有返回拖拽结果；输出片段：%s'
                             % (result.stdout[-400:] + result.stderr[-400:]))
        self.data = json.loads(raw)

    def test_the_pill_follows_the_pointer(self):
        self.assertEqual(self.data['attached'], '1')
        self.assertAlmostEqual(self.data['dx'], 200, delta=3)
        # 指针从 top+5 移到 top-155，差值就是 -160：气泡跟的是指针位移，不是绝对坐标。
        self.assertAlmostEqual(self.data['dy'], -160, delta=3)

    def test_dragging_switches_to_absolute_coordinates(self):
        self.assertTrue(self.data['inlineLeft'].endswith('px'))
        self.assertEqual(self.data['inlineRight'], 'auto')

    def test_the_position_is_remembered(self):
        if self.data['saved'] == 'unavailable':
            self.skipTest('这个浏览器环境下 file:// 页面没有 localStorage')
        saved = json.loads(self.data['saved'])
        self.assertAlmostEqual(saved['x'], float(self.data['inlineLeft'][:-2]), delta=1)
        self.assertAlmostEqual(saved['y'], float(self.data['inlineTop'][:-2]), delta=1)
        self.assertGreaterEqual(saved['x'], 0)
        self.assertGreaterEqual(saved['y'], 0)

    def test_the_click_after_a_drag_is_swallowed(self):
        self.assertTrue(self.data['clickPrevented'],
                        '拖完那次点击没被吞掉——想挪位置的人会被带到别的页面')


if __name__ == '__main__':
    unittest.main(verbosity=2)
