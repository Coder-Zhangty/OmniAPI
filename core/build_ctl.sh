#!/bin/sh
# Control the from-source New API build on the gateway host.
#
# Why this exists: a long docker build must not be tied to an SSH session (the
# session can drop mid-build), and a `pkill -f "docker build"` typed over SSH
# matches the SSH command line itself and kills the caller. Keeping the pattern
# inside a script avoids both problems.
#
#   sh build_ctl.sh status   # is anything building, and how far along
#   sh build_ctl.sh start    # start detached, log to $LOG
#   sh build_ctl.sh stop     # stop any running build (layer cache is kept)
#   sh build_ctl.sh tail     # follow the log briefly
set -eu

PAT='docker buil[d]'
LOG="${BUILD_LOG:-/root/check-build.log}"
CONTEXT="${BUILD_CONTEXT:-/opt/ai-gateway}"
TAG="${BUILD_TAG:-omniapi/new-api-check:rc40}"
DOCKERFILE="${BUILD_DOCKERFILE:-core/Dockerfile.check}"

running() { pgrep -cf "$PAT" 2>/dev/null || echo 0; }

case "${1:-status}" in
  status)
    echo "running builds: $(running)"
    if [ -f "$LOG" ]; then
        echo "log: $LOG ($(wc -l <"$LOG") lines, updated $(date -r "$LOG" -u +%H:%M:%SZ))"
        echo "--- last 15 lines ---"
        tail -15 "$LOG"
    else
        echo "no log at $LOG"
    fi
    ;;
  start)
    cd "$CONTEXT"
    rm -f "$LOG"
    setsid nohup docker build -f "$DOCKERFILE" -t "$TAG" "${2:-/opt/new-api-src}" \
        >"$LOG" 2>&1 </dev/null &
    sleep 8
    echo "started; running builds: $(running)"
    tail -5 "$LOG"
    ;;
  stop)
    for p in $(pgrep -f "$PAT" 2>/dev/null || true); do
        [ "$p" = "$$" ] && continue
        kill "$p" 2>/dev/null || true
    done
    sleep 3
    echo "stopped; running builds: $(running)"
    ;;
  tail)
    tail -25 "$LOG"
    ;;
  *)
    echo "usage: $0 {status|start|stop|tail}" >&2
    exit 2
    ;;
esac
