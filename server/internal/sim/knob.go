// Package sim 实现游戏规则：移动、战斗、死亡复活、板子翻窗、道具、质量派生。
// 数值唯一真相：D0001（此处只引用 KNOB 常量，改数只改本文件顶部，须同步 D0001）。
// 硬约束（BE0000M05⑧）：本包禁止浮点（全部 pkg/fixed）；不 import proto（T0004M02）。
package sim

import "soupout-server/pkg/fixed"

// ---- KNOB（D0001，Q22.10 定点或整数毫秒） ----

// 主属性表锚点（D0001M02，按面积% 排序）：
//   面积% | 移速(单位/s) | 攻击倍率 | 受伤减免% | 翻窗(ms) | 体型(单位)
//       5 | 6.2          | 0.80     | 0%        | 350      | 0.70
//      10 | 6.0          | 0.85     | 0%        | 400      | 0.80
//      20 | 5.6          | 0.95     | 5%        | 650      | 1.00
//      30 | 5.2          | 1.05     | 10%       | 900      | 1.20
//      35 | 5.0          | 1.10     | 12%       | 1030     | 1.30
//      40 | 4.9          | 1.15     | 14%       | 1150     | 1.40
//      50 | 4.7          | 1.24     | 17%       | 1400     | 1.60
//      65 | 4.5          | 1.35     | 20%       | 1780     | 1.85
var (
	areaAnchor   = [...]int{5, 10, 20, 30, 35, 40, 50, 65}
	speedAnchor  = [...]int32{6200, 6000, 5600, 5200, 5000, 4900, 4700, 4500} // 千分比（×1024/1000 转定点）
	atkAnchor    = [...]int32{800, 850, 950, 1050, 1100, 1150, 1240, 1350}
	dmgRedAnchor = [...]int32{0, 0, 50, 100, 120, 140, 170, 200} // 百分比×10
	vaultAnchor  = [...]int32{350, 400, 650, 900, 1030, 1150, 1400, 1780} // 毫秒
	sizeAnchor   = [...]int32{700, 800, 1000, 1200, 1300, 1400, 1600, 1850} // 千分比
)

// anchor 把千分比表值转 Q22.10：v×1024/1000（整数运算，无浮点）。
func anchor(v int32) fixed.F { return fixed.F(v) * fixed.F(1024) / fixed.F(1000) }

// Attr 是玩家派生属性（每 tick 由面积质量重算）。
type Attr struct {
	Speed   fixed.F // 移速（世界单位/秒）
	AtkMul  fixed.F // 攻击倍率
	DmgRed  fixed.F // 受伤减免（百分比数值 0..20）
	VaultMS uint32  // 翻窗耗时（毫秒）
	Size    fixed.F // 体型/碰撞半径（单位）
	Heavy   bool    // 面积 ≥35%（滞回由 Game.heavyLock 管理）
}

// AttrsFor 按面积万分比查 LUT 线性插值（D0001M02）。
func AttrsFor(areaPermyriad uint16) Attr {
	a := int(areaPermyriad) / 100 // 面积百分比
	at := lookup(a)
	at.Heavy = a >= 35
	return at
}

func lookup(a int) Attr {
	if a <= areaAnchor[0] {
		return attrAt(0)
	}
	if a >= areaAnchor[len(areaAnchor)-1] {
		return attrAt(len(areaAnchor) - 1)
	}
	for i := 0; i < len(areaAnchor)-1; i++ {
		if a >= areaAnchor[i] && a < areaAnchor[i+1] {
			return interp(i, a)
		}
	}
	return attrAt(len(areaAnchor) - 1)
}

func attrAt(i int) Attr {
	return Attr{
		Speed:   anchor(speedAnchor[i]),
		AtkMul:  anchor(atkAnchor[i]),
		DmgRed:  fixed.I(dmgRedAnchor[i]) / 10,
		VaultMS: uint32(vaultAnchor[i]),
		Size:    anchor(sizeAnchor[i]),
	}
}

// interp 在区间 [i, i+1] 内对面积 a 线性插值（t = (a-a0)/(a1-a0) 定点）。
func interp(i, a int) Attr {
	a0, a1 := areaAnchor[i], areaAnchor[i+1]
	t := fixed.F(a-a0).Div(fixed.F(a1 - a0)) // [0,1) Q22.10
	lerp := func(v0, v1 int32) fixed.F {
		f0, f1 := anchor(v0), anchor(v1)
		return f0.Add(f1.Sub(f0).Mul(t))
	}
	return Attr{
		Speed:   lerp(speedAnchor[i], speedAnchor[i+1]),
		AtkMul:  lerp(atkAnchor[i], atkAnchor[i+1]),
		DmgRed:  fixed.I(dmgRedAnchor[i]).Add(fixed.I(dmgRedAnchor[i+1]-dmgRedAnchor[i]).Mul(t)) / 10,
		VaultMS: uint32(vaultAnchor[i]) + uint32(fixed.I(int32(vaultAnchor[i+1]-vaultAnchor[i])).Mul(t).ToInt()),
		Size:    lerp(sizeAnchor[i], sizeAnchor[i+1]),
	}
}

// ---- 战斗（D0001M06） ----

const (
	KNOB_HPMax          = 100
	KNOBAttackDamage    = 15
	KNOBAttackCDMS      = 450
	KNOBAttackWindupMS  = 150
	KNOBAttackRange     = fixed.F(2458) // 2.4 单位（Q22.10）
)

// ---- 死亡与复活（D0001M07） ----

const (
	KNOBRespawnInvulnMS = 1500
	// 流失：自身面积 1.5%/秒 ⇒ dr/dt = −0.0075·r 格/s = −0.003·r Chamfer/tick。
	// 换算见 DissolveRateFor。
)

// KNOBRespawnLadderMS 复活阶梯（第 3 次起封顶）。
var KNOBRespawnLadderMS = [3]uint32{3000, 5000, 8000}

// DissolveRateFor 按当前面积（格数）换算 territory.Dissolve 的每 tick 半径衰减（Chamfer）。
// dA/dt = −0.015·A ⇒ dr/dt = −0.0075·r 格/s；r = √(A/π) 格（π ≈ 3217/1024）；
// Chamfer/tick = 0.0075×8×r/20 = 0.003·r。
func DissolveRateFor(areaCells int32) fixed.F {
	r := fixed.I(areaCells).Div(fixed.F(3217)).Sqrt()
	return r.Mul(fixed.F(3)) // 0.003 ≈ 3.07 定点，取 3（误差 < 2.5%）
}

// ---- 板子（D0001M04） ----

const (
	KNOBPalletPushMS    = 250
	KNOBPalletStunMS    = 800
	KNOBPalletStunRange = fixed.F(1536) // 1.5 单位
	KNOBPalletBreakHits = 3
	// 禁板子阈值：面积 ≥ 35%（heavy）；滞回回落到 ≤ 30% 解锁（heavyUnlockAt）
	heavyUnlockAt = 3000 // 万分比
)

// ---- 扩张（D0001M05） ----

const (
	KNOBExpandRate       = fixed.F(64)  // 0.0625 Chamfer/tick（Q22.10）
	KNOBChargeMoveFactor = fixed.F(461) // 充能时移速 ×0.45
	KNOBMatchDurationTick = 3600 // 3:00
	KNOBEarlyWinPermyriad = 6500 // 65% 提前结束
)

// ---- 道具（D0001M08） ----

const (
	KNOBDropFirstMS    = 30_000
	KNOBDropIntervalMS = 20_000
	// 奶盖：护盾 50，8s；润甜：扩张 ×2.5，6s；酸菜：投掷不可生根 5s
	KNODShieldAmount  = 50
	KNODShieldMS      = 8000
	KNOBExpandBoost   = fixed.F(2560) // ×2.5
	KNOBExpandBoostMS = 6000
)

// 导出（供 room 层查询）。
const (
	AttackDamage       = KNOBAttackDamage
	AttackRange        = KNOBAttackRange
	MatchDurationTick  = KNOBMatchDurationTick
	EarlyWinPermyriad  = KNOBEarlyWinPermyriad
	HPMax              = KNOB_HPMax
)
