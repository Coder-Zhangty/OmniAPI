"""Explicit loopback-only demo data; never auto-created in production."""
import base64
import struct
import time
import zlib
from .store import digest, encode


def sample_png():
    """Small, valid raster attachment depicting a fictional wireframe."""
    width, height = 240, 150
    blocks = [(8, 8, 232, 142, (255, 255, 255)), (8, 8, 232, 28, (160, 174, 191)),
              (20, 36, 63, 132, (227, 234, 244)), (73, 36, 219, 71, (207, 219, 238)),
              (73, 81, 137, 126, (235, 239, 246)), (147, 81, 219, 126, (235, 239, 246)),
              (165, 16, 190, 19, (255, 255, 255)), (195, 16, 220, 19, (255, 255, 255))]
    raster = bytearray()
    for y in range(height):
        raster.append(0)
        for x in range(width):
            color = (245, 247, 250)
            for left, top, right, bottom, fill in blocks:
                if left <= x < right and top <= y < bottom:
                    color = fill
            raster.extend(color)
    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raster)) + chunk(b"IEND", b"")


def sample_pdf():
    page = b"BT /F1 24 Tf 48 746 Td (OmniAPI archive acceptance sample) Tj ET"
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        b"<< /Length " + str(len(page)).encode() + b" >>\nstream\n" + page + b"\nendstream",
    ]
    doc, offsets = bytearray(b"%PDF-1.4\n"), []
    for i, obj in enumerate(objects, 1):
        offsets.append(len(doc))
        doc.extend(f"{i} 0 obj\n".encode() + obj + b"\nendobj\n")
    xref = len(doc)
    doc.extend(b"xref\n0 6\n0000000000 65535 f \n")
    for offset in offsets:
        doc.extend(f"{offset:010d} 00000 n \n".encode())
    doc.extend(f"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode())
    return bytes(doc)


def seed(store):
    if store.directory()["users"]:
        return
    for i, (uid, project, conv, title, reply) in enumerate([
        (42,"官网重构","c001","帮我调整一下首页布局，导航更简洁，内容区留白多一点。","可以，建议先调整这三处：\n\n1. 收紧顶部导航，保留主要入口。\n2. 内容区统一左对齐，增加段落留白。\n3. 右侧信息按需展开，避免打断阅读。\n\n```css\n.workspace {\n  max-width: 1200px;\n  padding: 32px 40px;\n}\n```"),
        (42,"官网重构","c002","登录页面调整","验证码输入区域可以再清晰一点。"),
        (42,"官网重构","c003","接口与错误状态梳理","区分请求失败与客户端主动取消。"),
        (42,"官网重构","c004","组件间距与字体规范","统一组件间距，更新字体层级。"),
        (42,"官网重构","c005","响应式布局检查","在不同屏幕宽度下检查首页表现。"),
        (42,"官网重构","c006","导航目录整理","梳理导航层级与命名，保持结构清晰。"),
        (42,"","","未分类请求","缺少会话 ID 的请求保持未分类。"),
        (57,"设计项目","design-1","导出与附件验收","这是另一个合成用户的数据。"),
        (68,"开发测试","dev-1","异常状态验收","模拟失败，不调用真实上游。"),
        (91,"个人项目","personal-1","历史记录检索","中文短关键词也能被检索。"),
    ]):
        rid = digest(str(i).encode())[:32]
        created = time.time() - i * 2400 - (86400 if i >= 3 else 0)
        meta = {"archive_id":rid,"user_id":uid,"project_id":project,"conversation_id":conv,"created":created,"model":"GPT"}
        previous = ""
        payloads = [("request", {"input_format":"json","path":"/v1/chat/completions"}), ("input", {"model":"GPT","messages":[{"role":"user","content":title}]}), ("output", {"choices":[{"message":{"role":"assistant","content":reply,"tool_calls":[{"type":"function","function":{"name":"read_page","arguments":"{}"}}] if i==0 else []}}]}), ("final", {"http_status":500 if uid==68 else 200,"upstream_model":"GPT","finished":time.time()-i*2400})]
        for seq,(kind,payload) in enumerate(payloads):
            event = {"version":3,"meta":{**meta,"kind":kind,"seq":seq,"prev_hash":previous,"status":"failed" if uid==68 else "completed"},"data":base64.b64encode(encode(payload)).decode()}
            previous = store.ingest(encode(event))
        with store.db() as db:
            db.execute("UPDATE records SET updated=created WHERE id=?",(rid,))
            if i==0:
                db.execute("UPDATE records SET title=? WHERE id=?",('首页布局与交互优化',rid))
        if i==0:
            store.add_attachment(rid,"首页草图.png","image/png",sample_png())
            store.add_attachment(rid,"布局说明.pdf","application/pdf",sample_pdf())
    with store.db() as db:
        for uid,name in [(42,"张先生"),(57,"设计工作室"),(68,"开发测试"),(91,"个人项目")]:
            db.execute("UPDATE users SET name=? WHERE id=?",(name,uid))
