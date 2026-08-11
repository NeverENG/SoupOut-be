#!/usr/bin/env bash
## 本地端到端联机验证：Go 逻辑服 + BanNet serve + 4 个 Godot 客户端。
## 用法：bash tools/e2e_local.sh
## 前置：BanNet 已构建 examples/serve（cargo build --example serve）。
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
SERVER_ROOT="$(cd "$HERE/.." && pwd)"
SOUP_ROOT="$(cd "$HERE/../../.." && pwd)"
FE="$SOUP_ROOT/SoupOut-fe"
BANNET="$(cd "$SOUP_ROOT/../BanNet" && pwd)"

SOCK="/tmp/soup-e2e-$(date +%s).sock"
GO_LOG="/tmp/soup-go-e2e.log"
ENG_LOG="/tmp/soup-engine-e2e.log"
PROBE_LOG="/tmp/soup-probe"

echo "== 1/4 启动 Go 逻辑服 =="
( cd "$SERVER_ROOT" && go run ./cmd/server -socket "$SOCK" >"$GO_LOG" 2>&1 ) &
GO_PID=$!
sleep 1

echo "== 2/4 启动 BanNet 引擎 (SoupOut legacy 兼容参数) =="
( cd "$BANNET" && ./target/debug/examples/serve --bind 127.0.0.1:12345 --uds "$SOCK" --workers 2 --hmac false --plaintext true --frame-order chhigh >"$ENG_LOG" 2>&1 ) &
ENG_PID=$!
sleep 3

echo "== 3/4 启动 4 个 Godot 客户端 =="
pids=""
for i in 1 2 3 4; do
  godot --headless --path "$FE" -s res://tests/online_probe.gd "P$i" >"${PROBE_LOG}${i}.log" 2>&1 &
  pids="$pids $!"
  sleep 2
done

FAIL=0
for p in $pids; do
  wait "$p" || FAIL=1
done

echo "== 4/4 清理 =="
kill "$GO_PID" "$ENG_PID" 2>/dev/null

for i in 1 2 3 4; do
  echo "--- P$i ---"
  cat "${PROBE_LOG}${i}.log"
done

echo "--- Go 逻辑服日志 ---"
cat "$GO_LOG"

if [ "$FAIL" -eq 0 ]; then
  echo "==== E2E 通过：4 客户端全部进锅并收到快照 ===="
else
  echo "==== E2E 失败：查看上方日志 ===="
fi
exit "$FAIL"
