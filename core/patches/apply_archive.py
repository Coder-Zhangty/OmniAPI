"""Install only archive middleware and audited auth-chain anchors; safe to repeat."""
import argparse
from pathlib import Path

ROUTERS = ("router/relay-router.go", "router/task-plugin-protocol-router.go", "router/video-router.go")


def apply(source, check=False):
    source = Path(source)
    staged = {}
    total = 0
    for name in ROUTERS:
        path = source / name
        content = path.read_text(encoding="utf-8")
        # TokenAuth returns a Gin handler, making this insertion work for both
        # router.Use and literal handler slices, including plugin endpoints.
        for anchor in ("middleware.TokenAuth()", "middleware.UserAuth()"):
            marked = anchor + ", middleware.OmniArchive()"
            count = content.count(anchor)
            total += count
            content = content.replace(marked, anchor).replace(anchor, marked)
        if "middleware.OmniArchive()" not in content:
            raise RuntimeError("Archive authentication anchor drift: " + name)
        staged[path] = content
    if total < 10:
        raise RuntimeError("Incomplete archive route coverage")
    path = source / "controller/relay.go"
    if path.exists():
        content = path.read_text(encoding="utf-8")
        for function in ("relayHandler", "geminiRelayHandler"):
            anchor = f"func {function}(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {{"
            marked = anchor + '\n\tdefer func() { c.Set("omni_archive_upstream_model", info.GetUpstreamModelName()) }()'
            if marked not in content:
                if content.count(anchor) != 1:
                    raise RuntimeError("Archive model anchor drift: " + function)
                content = content.replace(anchor, marked)
        staged[path] = content
    staged[source / "middleware/omniapi_archive.go"] = (Path(__file__).parents[1] / "overlay/middleware/omniapi_archive.go").read_text(encoding="utf-8")
    if not check:
        for path, content in staged.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8", newline="\n")
    print(("CHECK" if check else "APPLIED"), len(staged), "archive source files")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    apply(args.source, args.check)
