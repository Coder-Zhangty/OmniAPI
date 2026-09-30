"""Generate inventories and scan deliverable files against local known secrets."""
import json
import subprocess
from gateway import ROOT, credentials, env_read, write_json


def main():
    cmd = ["git", "-c", "safe.directory=" + ROOT.as_posix(), "-C", str(ROOT)]
    files = subprocess.check_output(cmd + ["ls-files", "--cached", "--others", "--exclude-standard", "-z"]).decode().split("\0")
    files = sorted(set(f for f in files if f))
    values = [env_read()[k] for k in ("DB_PASSWORD", "REDIS_PASSWORD", "SESSION_SECRET", "CRYPTO_SECRET")]
    for name in ("admin", "tester", "test-token"):
        values += [v for k, v in credentials(ROOT, name).items() if k in ("password", "key")]
    provider_available = (ROOT / '.secrets/provider-default.json').exists()
    if provider_available:
        values.append(credentials(ROOT, 'provider-default')['api_key'])
    leaks = [name for name in files if any(v.encode() in (ROOT / name).read_bytes() for v in values)]
    if leaks:
        raise RuntimeError("Secret matches in: " + ", ".join(leaks))
    write_json(ROOT / "docs/SECRET_SCAN.json", {"status": "PASS", "files_scanned": len(files),
               "known_secret_matches": 0, "real_provider_key_available": provider_available})
    files = sorted(set(files + ["docs/FILES.md", "docs/CONTAINERS.md", "docs/SECRET_SCAN.json"]))
    text = "# 文件清单\n\n工作目录原先为空，以下交付文件均为本次新增。\n\n"
    text += "".join("- `" + name + "`\n" for name in files)
    # Derive the backup line from disk; a hardcoded timestamp goes stale silently.
    backup_dirs = sorted(p.name for p in (ROOT / "backups").glob("*") if p.is_dir())
    if backup_dirs:
        backup_line = ("- `backups/`：已验证的主环境备份共 " + str(len(backup_dirs))
                       + " 份，最新 `backups/" + backup_dirs[-1]
                       + "/`，含数据库、应用数据、恢复配置和校验清单。\n")
    else:
        backup_line = "- `backups/`：暂无备份。\n"
    text += """
## 仅本机生成、已排除 Git

- `.env`：运行配置与数据库、Redis、Session、Crypto 秘密。
- `.secrets/admin.json`、`tester.json`、`test-token.json`、`bootstrap.json`：账户、Token 及初始化标记。
""" + backup_line + """- `runtime/`：健康检查、验收证据、隔离模拟与恢复环境。
- `.research/`：核查的官方源码及安装校验资料；安装程序及分段缓存已清理。
- `.git/`、`__pycache__/`：仓库元数据与 Python 缓存。

Docker Desktop 安装于当前用户 Programs/DockerDesktop，使用已有 WSL2；New API 核心源码未修改。
"""
    (ROOT / "docs/FILES.md").write_text(text, encoding="utf-8")
    rows = json.loads((ROOT / "runtime/container-inventory.json").read_text(encoding="utf-8"))
    text = "# Docker 容器清单\n\n仅主环境四个容器运行；测试和恢复环境已停止，数据卷保留供复核。\n\n| 容器 | 状态 | 端口显示 |\n|---|---|---|\n"
    for row in rows:
        text += "| " + row["name"] + " | " + row["state"] + " | " + (row["ports"] or "无活动发布端口") + " |\n"
    (ROOT / "docs/CONTAINERS.md").write_text(text, encoding="utf-8")
    print("Delivery secret scan: PASS; inventory files:", len(files))


if __name__ == "__main__":
    main()
