// Package lobby 实现 Gatekeeper（T0004 的 lobby 层）：
// 会话认证 + 路由 + 建房工厂。单房间模型：所有玩家进同一房间，
// 第 4 人 OnJoin 触发开局（房间实现侧）。大厅完整流程（房间码/
// 快速匹配/选料/ready）留待联调阶段，见 AGENTS.md。
package lobby

import (
	"soupout-server/internal/room"

	"github.com/NeverENG/BanNet/soup-sdk-go"
)

// Gatekeeper 是单房间 Gatekeeper。
type Gatekeeper struct {
	next soup.PlayerID
}

// Authenticate 全部放行，按 0..3 轮转分配 PlayerID（单房间最多 4 人）。
func (g *Gatekeeper) Authenticate(token []byte, addr string) *soup.PlayerID {
	p := g.next
	g.next = (g.next + 1) % 4
	return &p
}

// Route 恒路由到固定房间（已存在则加入；首个玩家触发建房）。
func (g *Gatekeeper) Route(p soup.PlayerID, hint soup.JoinHint) soup.RoomRoute {
	return soup.RoomRoute{Action: soup.RouteCreate, RoomID: 1}
}

// NewRoom 创建一局游戏房间。
func (g *Gatekeeper) NewRoom(id soup.RoomID, cfg any, players []soup.PlayerID, seed uint64) soup.Room {
	return room.NewGameRoom(room.Config{
		GridW:     96,
		GridH:     96,
		StewTicks: 4800, // 4 分钟 × 20Hz
		Seed:      seed,
	})
}
