#!/usr/bin/env python3
"""Read existing replica tunnel, build and atomically publish public index files.

The only SQL transaction is READ ONLY. No core restart, DDL, user data, billing
fields, credentials export or change to original /api/rankings is involved.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

from snapshot import canonical, freeze_history, generate, validate_raw

QUERY = """
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout='2500ms';
SET LOCAL lock_timeout='150ms';
SET LOCAL work_mem='4MB';
SET LOCAL max_parallel_workers_per_gather=0;
SELECT json_build_object(
 'recovery', pg_is_in_recovery(),
 'lag_seconds', CASE WHEN pg_last_xact_replay_timestamp() IS NULL THEN NULL
                   ELSE greatest(0,extract(epoch from now()-pg_last_xact_replay_timestamp())) END,
 'as_of', floor(extract(epoch from now()))::bigint,
 'rows', (SELECT json_agg(json_build_array(created_at,model_name,tokens) ORDER BY created_at,model_name)
 FROM (SELECT created_at,model_name,sum(token_used)::bigint tokens FROM quota_data
 WHERE model_name<>'' AND created_at <= floor(extract(epoch from now()))
 GROUP BY created_at,model_name HAVING sum(token_used)>0) q));
COMMIT;
"""


def atomic(path, data, mode):
    path = Path(path)
    fd, temporary = tempfile.mkstemp(prefix="." + path.name + "-", dir=path.parent)
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def read_replica():
    # Password stays on the server in the existing database container environment.
    # Endpoint is the existing reverse SSH tunnel to the PHYSICAL STANDBY.
    command = ['docker', 'exec', '-i', 'omni-ha-database-1', 'sh', '-c',
               'export PGPASSWORD="$POSTGRES_PASSWORD"; exec psql -h 172.30.10.1 -p 15433 -U gateway -d gateway -XAtq -v ON_ERROR_STOP=1 -f -']
    result = subprocess.run(command, input=QUERY, capture_output=True, text=True, timeout=8)
    if result.returncode:
        raise RuntimeError("bounded replica read failed; previous snapshot retained")
    if len(result.stdout) > 16 * 1024 * 1024:
        raise RuntimeError("aggregate size exceeds reviewed limit")
    raw = json.loads(result.stdout)
    validate_raw(raw)
    return raw


def run(args):
    state = args.state.resolve()
    assert state.is_dir() and state.stat().st_mode & 0o077 == 0, "private state permissions"
    with (state / "worker.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return {"skipped": "already running"}
        config = json.loads(args.config.read_text())
        raw = read_replica() if not args.input else json.loads(args.input.read_text())
        validate_raw(raw)
        previous_raw = state / "raw-last.json"
        if previous_raw.exists():
            before = json.loads(previous_raw.read_text())
            assert raw["as_of"] >= before["as_of"], "replica time moved backwards"
            assert min(r[0] for r in raw["rows"]) <= min(r[0] for r in before["rows"]), "real history truncated"
        history_file = state / "history.json"
        if not history_file.exists():
            assert args.init_history, "history not initialised; explicit first-run flag required"
            history = freeze_history(raw, config, {})
            atomic(history_file, canonical(history), 0o600)
        history = json.loads(history_file.read_text())
        manifest, files, audit = generate(raw, config, {}, history)
        public = args.public.resolve()
        assert public.is_dir() and public.name == "rankings"
        versions = public / "versions"
        versions.mkdir(exist_ok=True, mode=0o755)
        versions.chmod(0o755)
        final = versions / manifest["version"]
        temporary = versions / (".stage-" + manifest["version"])
        temporary.mkdir(mode=0o755, exist_ok=False)
        # systemd intentionally uses UMask=0077 for private state. Public
        # directories need explicit traverse permissions before manifest switch.
        temporary.chmod(0o755)
        try:
            for period, content in files.items():
                atomic(temporary / (period + ".json"), content, 0o644)
            if final.exists():
                assert all((final / (p + ".json")).read_bytes() == content for p, content in files.items())
                shutil.rmtree(temporary)
            else:
                os.rename(temporary, final)
            # Audit durable before exposing new manifest; no private fields served.
            audits = state / "audits"
            audits.mkdir(exist_ok=True, mode=0o700)
            atomic(audits / (manifest["version"] + ".json"), canonical(audit), 0o600)
            atomic(previous_raw, canonical(raw), 0o600)
            manifest_path = public / "manifest.json"
            if manifest_path.exists():
                old = manifest_path.read_bytes()
                if json.loads(old)["version"] != manifest["version"]:
                    atomic(state / "manifest.previous.json", old, 0o600)
            atomic(manifest_path, canonical(manifest), 0o644)
        finally:
            if temporary.exists():
                shutil.rmtree(temporary)
        # Retain two days of immutable versions; current/previous manifests pinned.
        pinned = {manifest["version"]}
        if (state / "manifest.previous.json").exists():
            pinned.add(json.loads((state / "manifest.previous.json").read_text())["version"])
        for directory in versions.iterdir():
            if directory.is_dir() and not directory.is_symlink() and not directory.name.startswith("."):
                if directory.name not in pinned and directory.stat().st_mtime < time.time() - 2 * 86400:
                    assert directory.resolve().parent == versions.resolve()
                    shutil.rmtree(directory)
        return {"published": True, "version": manifest["version"], "periods": len(files),
                "raw_rows": len(raw["rows"]), "replica_lag_seconds": raw["lag_seconds"]}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--public", type=Path, required=True)
    parser.add_argument("--input", type=Path)
    parser.add_argument("--init-history", action="store_true")
    try:
        print(json.dumps(run(parser.parse_args())))
    except Exception as error:
        # No raw SQL stderr or credentials in journals.
        print(json.dumps({"published": False, "error_type": type(error).__name__, "previous_snapshot_retained": True}), file=sys.stderr)
        raise SystemExit(1)
