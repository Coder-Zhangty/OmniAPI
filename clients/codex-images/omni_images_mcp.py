"""OmniAPI image generation for Codex, JSON-RPC over MCP stdio. Python 3.10+."""
import base64
import binascii
import json
import os
from pathlib import Path
import re
import struct
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid
import zlib

VERSION = '1.2.1'
MODEL = 'gpt-image-2'
MAX_RESPONSE = 96 * 1024 * 1024
PROTOCOLS = {'2024-11-05', '2025-03-26', '2025-06-18', '2025-11-25'}


class ImageError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def configuration():
    origin = os.environ.get('OMNI_BASE_URL', 'https://omniapi.biz/v1').rstrip('/')
    parsed = urllib.parse.urlsplit(origin)
    loopback = parsed.hostname in ('127.0.0.1', 'localhost', '::1')
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ImageError('API 地址不能包含登录信息、查询参数或片段。')
    if not parsed.hostname or (parsed.scheme != 'https' and not (loopback and parsed.scheme == 'http')):
        raise ImageError('API 地址必须使用 HTTPS。')
    key = os.environ.get('OMNI_API_KEY', '').strip()
    if not key or '\n' in key or '\r' in key:
        raise ImageError('请在 MCP 环境变量中设置 OMNI_API_KEY 为您的 OmniAPI 令牌。')
    directory = Path(os.environ.get('OMNI_IMAGE_OUTPUT_DIR', str(Path.home() / 'Pictures' / 'OmniAPI'))).expanduser().resolve()
    return origin, key, directory


def validate_png(data):
    if not data.startswith(b'\x89PNG\r\n\x1a\n'):
        raise ImageError('图片接口没有返回 PNG，未保存为成功图片。')
    pos = 8
    dimensions = None
    has_data = False
    while pos + 12 <= len(data):
        length, = struct.unpack('>I', data[pos:pos + 4])
        end = pos + 12 + length
        if end > len(data):
            break
        kind = data[pos + 4:pos + 8]
        payload = data[pos + 8:pos + 8 + length]
        crc, = struct.unpack('>I', data[pos + 8 + length:end])
        if zlib.crc32(kind + payload) & 0xffffffff != crc:
            raise ImageError('返回的 PNG 校验失败，请将 Request ID 发给客服。')
        if pos == 8:
            if kind != b'IHDR' or length != 13:
                break
            dimensions = struct.unpack('>II', payload[:8])
        if kind == b'IDAT':
            has_data = True
        if kind == b'IEND' and length == 0 and end == len(data) and has_data and dimensions:
            if min(dimensions) < 1 or max(dimensions) > 16384:
                break
            return dimensions
        pos = end
    raise ImageError('图片返回不完整，未保存为成功图片。')


def image_catalog():
    origin, key, _ = configuration()
    root = origin[:-3] if origin.endswith('/v1') else origin
    request = urllib.request.Request(root + '/api/omni/pricing', headers={'Authorization': 'Bearer ' + key})
    handlers = [NoRedirect()]
    if urllib.parse.urlsplit(origin).hostname in ('127.0.0.1', 'localhost', '::1'):
        handlers.append(urllib.request.ProxyHandler({}))
    try:
        with urllib.request.build_opener(*handlers).open(request, timeout=20) as response:
            raw = response.read(4 * 1024 * 1024 + 1)
        if len(raw) > 4 * 1024 * 1024:
            raise ValueError('oversized catalog')
        payload = json.loads(raw)
        models = payload.get('data', {}).get('models', {})
        return {name: row for name, row in models.items() if isinstance(row, dict) and row.get('image_specs') and row.get('availability', 'available') == 'available'}
    except (ValueError, AttributeError, urllib.error.URLError, TimeoutError, ConnectionError):
        raise ImageError('无法读取已验收图片规格，尚未提交生图请求，请稍后重试。') from None


def generate(arguments):
    allowed = {'prompt', 'filename', 'quality', 'model', 'size', 'service_tier', 'speed'}
    if not isinstance(arguments, dict) or set(arguments) - allowed:
        raise ImageError('包含未知参数，请使用工具列表中的图片参数。')
    prompt = arguments.get('prompt')
    if not isinstance(prompt, str) or not prompt.strip() or len(prompt) > 32000:
        raise ImageError('请输入 1–32000 字符的图片描述。')
    quality = arguments.get('quality', 'low')
    if quality not in ('low', 'medium', 'high', 'default'):
        raise ImageError('quality 请选择 low（基础）、medium（标准）或 high（精细）。')
    filename = arguments.get('filename', 'image-' + uuid.uuid4().hex[:12] + '.png')
    if not isinstance(filename, str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,115}\.png', filename):
        raise ImageError('filename 只能是英文字母、数字、点、下划线或连字符组成的 PNG 文件名。')
    if filename.split('.', 1)[0].upper() in {'CON', 'PRN', 'AUX', 'NUL', *(f'COM{i}' for i in range(10)), *(f'LPT{i}' for i in range(10))}:
        raise ImageError('请使用普通 PNG 文件名。')
    origin, key, directory = configuration()
    target = directory / filename
    if target.exists():
        raise ImageError('同名图片已存在，请换一个 filename；工具不会覆盖已有文件。')
    model = arguments.get('model', MODEL)
    size = arguments.get('size', '1024x1024')
    if not isinstance(model, str) or not re.fullmatch(r'[A-Za-z0-9_.-]{1,128}', model):
        raise ImageError('请选择模型目录中的图片型号。')
    if not isinstance(size, str) or not re.fullmatch(r'[1-9][0-9]{2,4}x[1-9][0-9]{2,4}', size):
        raise ImageError('尺寸应为模型目录中的像素尺寸，例如 1024x1024。')
    catalog = image_catalog()
    quote = catalog.get(model)
    if not quote:
        raise ImageError('该图片型号尚未开放，请先调用 omni_list_image_models。')
    specs = quote['image_specs']
    if 'size' not in arguments:
        size = specs.get('size', '1024x1024')
    if 'quality' not in arguments:
        quality = specs.get('default_quality', 'low')
    sizes = specs.get('sizes') or [specs.get('size', '1024x1024')]
    if size not in sizes or quality not in specs.get('qualities', ['low']):
        raise ImageError('此尺寸或品质尚未验收，请先调用 omni_list_image_models。')
    options = specs.get('options', [])
    if options and not any(o.get('size') == size and o.get('quality') == quality for o in options):
        raise ImageError('此尺寸与品质组合尚未开放。')
    body = {'model': model, 'prompt': prompt, 'n': 1, 'size': size, 'quality': quality, 'output_format': 'png'}
    for field in ['service_tier', 'speed']:
        if field in arguments:
            value = arguments[field]
            if not isinstance(value, str) or value not in quote.get('request_options', {}).get(field, []):
                raise ImageError(field + ' 尚未核价开放，未提交生图请求。')
            body[field] = value
    request = urllib.request.Request(origin + '/images/generations', data=json.dumps(body).encode(), headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json', 'User-Agent': 'OmniAPI-Codex-Images/' + VERSION})
    request_id = ''
    try:
        handlers = [NoRedirect()]
        if urllib.parse.urlsplit(origin).hostname in ('127.0.0.1', 'localhost', '::1'):
            handlers.append(urllib.request.ProxyHandler({}))
        with urllib.request.build_opener(*handlers).open(request, timeout=600) as response:
            request_id = response.headers.get('X-Oneapi-Request-Id', '')
            raw = response.read(MAX_RESPONSE + 1)
            if len(raw) > MAX_RESPONSE:
                raise ImageError('图片响应过大，未保存。')
    except urllib.error.HTTPError as error:
        request_id = error.headers.get('X-Oneapi-Request-Id', '')
        message = '图片请求失败，HTTP ' + str(error.code)
        try:
            data = json.loads(error.read(16384))
            detail = data.get('error', {})
            if isinstance(detail, dict) and isinstance(detail.get('message'), str):
                message += '：' + detail['message'].replace(key, '<REDACTED>')[:500]
        except (ValueError, AttributeError):
            pass
        raise ImageError(message + ('；Request ID: ' + request_id if request_id else '')) from None
    except (TimeoutError, urllib.error.URLError, ConnectionError):
        raise ImageError('图片接口连接中断或超时。工具不会自动重试，避免重复生成和重复费用；请先核对调用记录。') from None
    try:
        data = json.loads(raw)
        images = data.get('data', [])
        if len(images) != 1 or not isinstance(images[0].get('b64_json'), str):
            raise ImageError('接口没有返回单张 base64 图片，未保存为成功图片。')
        png = base64.b64decode(images[0]['b64_json'], validate=True)
        width, height = validate_png(png)
        if (width, height) != tuple(map(int, size.split('x'))):
            raise ImageError('实际图片尺寸与请求不符，未保存为成功图片；Request ID: ' + request_id)
    except (ValueError, KeyError, AttributeError, TypeError, binascii.Error):
        raise ImageError('图片响应格式错误，未保存为成功图片。') from None
    directory.mkdir(parents=True, exist_ok=True)
    # Exclusive creation protects an existing file even if another call won the race.
    with target.open('xb') as file:
        file.write(png)
    return {'model': model, 'quality': quality, 'size': size, 'path': str(target), 'width': width, 'height': height, 'request_id': request_id, 'message': '图片已生成并保存。向用户展示此本地 PNG 链接即可，无需再次读取图片或再次调用生图工具。'}


def handle(message):
    if not isinstance(message, dict) or message.get('jsonrpc') != '2.0' or not isinstance(message.get('method'), str):
        return {'jsonrpc': '2.0', 'id': message.get('id') if isinstance(message, dict) else None, 'error': {'code': -32600, 'message': 'Invalid Request'}}
    if 'id' not in message:
        return None
    method = message['method']
    params = message.get('params') or {}
    if method == 'initialize':
        version = params.get('protocolVersion', '')
        result = {'protocolVersion': version if version in PROTOCOLS else '2025-06-18', 'capabilities': {'tools': {}}, 'serverInfo': {'name': 'omniapi-images', 'version': VERSION}, 'instructions': 'Use omni_generate_image when the user requests an image. This tool saves a real PNG locally. Return its absolute file link. Do not replace the Codex conversation model or automatically retry image generation.'}
    elif method == 'ping':
        result = {}
    elif method == 'tools/list':
        result = {'tools': [
            {'name': 'omni_generate_image', 'description': '生成并保存一张真实 PNG；先用 omni_list_image_models 查看已验收型号、尺寸、品质和价格。默认型号 gpt-image-2，尺寸和品质按实时目录。严格保留用户参数，不自动重试。', 'inputSchema': {'type': 'object', 'properties': {
                'prompt': {'type': 'string', 'minLength': 1, 'maxLength': 32000},
                'model': {'type': 'string', 'default': MODEL},
                'size': {'type': 'string', 'description': '省略时使用实时目录中的型号默认尺寸；显式指定的尺寸不会被改写'},
                'quality': {'type': 'string', 'enum': ['low', 'medium', 'high', 'default'], 'description': '省略时使用该型号已验收的默认品质；旧配置默认为 low'},
                'service_tier': {'type': 'string', 'description': '仅限目录明确开放的服务档位'},
                'speed': {'type': 'string', 'description': '仅限目录明确开放的速度档位'},
                'filename': {'type': 'string', 'description': '仅 PNG 文件名；拒绝覆盖已有文件'}
            }, 'required': ['prompt'], 'additionalProperties': False}, 'annotations': {'readOnlyHint': False, 'destructiveHint': False, 'idempotentHint': False, 'openWorldHint': True}},
            {'name': 'omni_list_image_models', 'description': '读取当前已验收的生图型号、尺寸、品质与公开价格，不生成图片。', 'inputSchema': {'type': 'object', 'properties': {}, 'additionalProperties': False}, 'annotations': {'readOnlyHint': True, 'openWorldHint': True}}
        ]}
    elif method == 'tools/call':
        try:
            if params.get('name') == 'omni_list_image_models':
                value = image_catalog()
            elif params.get('name') == 'omni_generate_image':
                value = generate(params.get('arguments', {}))
            else:
                raise ImageError('未知图片工具。')
            result = {'content': [{'type': 'text', 'text': json.dumps(value, ensure_ascii=False)}], 'isError': False}
        except (ImageError, OSError) as error:
            result = {'content': [{'type': 'text', 'text': str(error) if isinstance(error, ImageError) else '无法将图片保存到输出目录，请检查目录权限。'}], 'isError': True}
    else:
        return {'jsonrpc': '2.0', 'id': message['id'], 'error': {'code': -32601, 'message': 'Method not found'}}
    return {'jsonrpc': '2.0', 'id': message['id'], 'result': result}


def main():
    # stdout contains JSON-RPC frames only. No keys, prompts or HTTP tracebacks are logged.
    for raw in sys.stdin.buffer:
        try:
            if len(raw) > 256 * 1024:
                raise ValueError('oversized message')
            reply = handle(json.loads(raw))
        except (ValueError, TypeError, AttributeError):
            reply = {'jsonrpc': '2.0', 'id': None, 'error': {'code': -32700, 'message': 'Parse error'}}
        if reply is not None:
            print(json.dumps(reply, ensure_ascii=True), flush=True)


if __name__ == '__main__':
    main()
