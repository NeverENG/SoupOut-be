#!/usr/bin/env bash
## 多人联机验收：Go 逻辑服 + BanNet 引擎 + 4 个 Godot 客户端（无头驻留探针）。
## 与 e2e_local.sh 的区别：
##   1. 先编译再启动（go run 冷启动 >1s，引擎会拨到还不存在的 socket）
##   2. 等 socket 真的出现，而不是 sleep 猜
##   3. 用 online_hold_probe.gd：进局后驻留 15s，校验 4 人 / tick 推进 / 地盘落地 / 远端可见
## 用法：bash tools/e2e_multi.sh
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
SERVER_ROOT="$(cd "$HERE/.." && pwd)"
SOUP_ROOT="$(cd "$HERE/../../.." && pwd)"
FE="$SOUP_ROOT/SoupOut-fe"
BANNET="$(cd "$SOUP_ROOT/../BanNet" && pwd)"

RUN_DIR="${RUN_DIR:-/tmp/soup-e2e}"
mkdir -p "$RUN_DIR"
SOCK="$RUN_DIR/soup.sock"
GO_LOG="$RUN_DIR/go.log"
ENG_LOG="$RUN_DIR/engine.log"
GO_BIN="$RUN_DIR/soup-server"
ENG_BIN="$BANNET/target/debug/examples/serve"

rm -f "$SOCK" "$GO_LOG" "$ENG_LOG" "$RUN_DIR"/probe*.log

cleanup() { kill "${GO_PID:-}" "${ENG_PID:-}" 2>/dev/null; }
trap cleanup EXIT

echo "== 1/5 编译 Go 逻辑服（vendor 模式，带 SDK 本地修复）=="
( cd "$SERVER_ROOT" && go build -o "$GO_BIN" ./cmd/server ) || { echo "go build 失败"; exit 1; }

echo "== 2/5 检查引擎二进制 =="
if [ ! -x "$ENG_BIN" ]; then
  echo "缺少 $ENG_BIN，请先在 BanNet 执行: cargo build --example serve"
  exit 1
fi

echo "== 3/5 启动 Go 逻辑服 =="
SOUP_DEBUG="${SOUP_DEBUG:-1}" "$GO_BIN" -socket "$SOCK" >"$GO_LOG" 2>&1 &
GO_PID=$!
for _ in $(seq 1 50); do
  [ -S "$SOCK" ] && break
  sleep 0.2
done
if [ ! -S "$SOCK" ]; then
  echo "逻辑服没建出 socket:"; cat "$GO_LOG"; exit 1
fi

echo "== 4/5 启动 BanNet 引擎（legacy 兼容参数）=="
"$ENG_BIN" --bind 127.0.0.1:12345 --uds "$SOCK" --workers 2 \
  --hmac false --plaintext true --frame-order chhigh >"$ENG_LOG" 2>&1 &
ENG_PID=$!
for _ in $(seq 1 50); do
  grep -q "engine" "$ENG_LOG" 2>/dev/null && break
  sleep 0.2
done
sleep 1
if ! kill -0 "$ENG_PID" 2>/dev/null; then
  echo "引擎退出了:"; cat "$ENG_LOG"; exit 1
fi

## VISUAL=1：带窗口跑并各自截图到 $RUN_DIR/shotN.png（无头全绿 ≠ 画面对）
echo "== 5/5 启动 4 个 Godot 客户端（驻留探针，VISUAL=${VISUAL:-0}）=="
pids=""
for i in 1 2 3 4; do
  if [ "${VISUAL:-0}" = "1" ]; then
    godot --path "$FE" --resolution 960x600 --position $(( (i-1)%2 * 970 )),$(( (i-1)/2 * 620 )) \
      -s res://tests/online_hold_probe.gd -- "P$i" "$RUN_DIR/shot$i.png" \
      >"$RUN_DIR/probe$i.log" 2>&1 &
  else
    godot --headless --path "$FE" -s res://tests/online_hold_probe.gd -- "P$i" \
      >"$RUN_DIR/probe$i.log" 2>&1 &
  fi
  pids="$pids $!"
  sleep 1
done

FAIL=0
for p in $pids; do
  wait "$p" || FAIL=1
done

for i in 1 2 3 4; do
  echo "--- P$i ---"
  grep -E "已发起|进局|观测|PASS|FAIL|transport" "$RUN_DIR/probe$i.log" || tail -5 "$RUN_DIR/probe$i.log"
done

echo "--- Go 逻辑服（后 25 行）---"
tail -25 "$GO_LOG"
echo "--- keyframe 发送次数（每帧全量 = ACK 没推进）---"
grep -c -i "keyframe" "$GO_LOG" || true

if [ "$FAIL" -eq 0 ]; then
  echo "==== 多人联机 E2E 通过 ===="
else
  echo "==== 多人联机 E2E 失败：日志在 $RUN_DIR ===="
fi
exit "$FAIL"
