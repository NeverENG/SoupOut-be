package sim

// Player 是单个玩家的运行时状态（固定 4 人，数组存储零分配）。
import (
	"soupout-server/pkg/fixed"
)

// Player 状态（坐标世界单位，Q22.10 定点；协议 u16 1/64 量化在 room 层转换）。
type Player struct {
	ID uint8 // 1..4

	Pos fixed.Vec2
	Vel fixed.Vec2
	Aim fixed.F // 弧度 [0, 2π)（Q22.10，2π≈6.28 < 2048 ✓）

	Mass fixed.F // 质量 = 面积万分比 × 100 / 10000 × 100…… 由 AreaPermyriad 派生

	HP      int32
	Shield  int32 // 奶盖护盾（剩余吸收量）
	Dead    bool
	AliveMS uint32 // 死亡已持续毫秒（复活计时）
	DeathCount int32

	// 攻击
	AttackCDMS   uint32 // 距上次攻击剩余 cd（毫秒）
	AttackWindupMS uint32 // 前摇剩余（>0 = 正在前摇，前摇结束出伤害）
	// 攻击目标（前摇锁定）
	WindupTarget uint8

	// 充能（D0001M05：充能可缓慢移动、不可攻击）
	Charging bool

	// 翻窗（D0001M03）
	Vaulting bool
	VaultMS  uint32 // 剩余翻窗耗时
	VaultID  uint8

	// 道具 buff 剩余毫秒
	ShieldMS  uint32
	ExpandMS  uint32

	// 滞回：过重锁（面积 ≥35% 锁，回落 ≤30% 解锁）
	HeavyLock bool

	// 复活无敌剩余毫秒
	InvulnMS uint32
}

// Reset 在开局/复活时初始化。
func (p *Player) Reset(id uint8, pos fixed.Vec2) {
	*p = Player{ID: id, Pos: pos, HP: KNOB_HPMax}
}

// EffectiveAttrs 计算当前派生属性（room 层每 tick 传入面积万分比后调用）。
func (p *Player) EffectiveAttrs(areaPermyriad uint16) Attr {
	at := AttrsFor(areaPermyriad)
	// 滞回：≥35% 锁重装，≤30% 解锁（D0001M04）
	if at.Heavy {
		p.HeavyLock = true
	} else if int(areaPermyriad) <= heavyUnlockAt {
		p.HeavyLock = false
	}
	at.Heavy = p.HeavyLock
	p.Mass = fixed.I(int32(areaPermyriad)) * 100 / 10000 // 质量 = 面积%×100（Q22.10）
	return at
}
