// Command server 是 SoupOut 逻辑服主程序：
// 装配 BanNet SDK（UDS 监听，引擎拨号）+ Gatekeeper（lobby）。
//
// 用法：
//
//	go run ./cmd/server -socket /tmp/soup.sock
//
// 然后启动引擎（引擎主动连接该 UDS 路径）。
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"soupout-server/internal/lobby"
	"soupout-server/internal/adapter"

	"github.com/NeverENG/BanNet/soup-sdk-go"
)

func main() {
	socket := flag.String("socket", "/tmp/soup.sock", "引擎连接用 UDS 路径（SDK 监听）")
	flag.Parse()

	srv := soup.NewServer(
		soup.WithEngineSocket(*socket),
		soup.WithTickHz(20),
		soup.WithSnapshotHz(10),
		soup.WithGatekeeper(&lobby.Gatekeeper{}),
		// P0 旧 Godot 客户端适配:输入 30B 帧头由游戏侧 codec 解析,
		// 快照/全量消息号映射 0x0C0/0x042。正式客户端可去掉这些开关。
		soup.WithInputCodec(adapter.LegacyInputCodec{}),
		soup.WithSnapshotMsgID(0x0C0),
		soup.WithFullStateMsgID(0x042),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("soup-server: listening on %s (waiting for engine)", *socket)
	if err := srv.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
