package sim

// Game 是一局的核心状态机：玩家 + 地盘场 + 规则推进。
// room 层每 tick 调 Step 并取 Events 转协议消息。
// 依赖：territory（地盘场）+ fixed；不 import proto（T0004M02）。

import (
	"soupout-server/internal/territory"
	"soupout-server/pkg/fixed"
)

// Input 是 room 层转换后的单玩家本 tick 输入（sim 不 import proto）。
type Input struct {
	MoveX, MoveY int8   // -100..100
	Aim          uint16 // 0..65535 → 0..2π
	Buttons      uint8  // 位定义与 T0001M02F04 一致
	Has          bool   // 本 tick 是否收到输入（未收到 = 0 移动）
}

// EventKind 是 sim 输出的事件类型（room 层转 proto 消息）。
type EventKind uint8

const (
	EvNone EventKind = iota
	EvPlayerDied
	EvPlayerRespawn
	EvPalletDown
	EvDropSpawn
	EvDropTaken
	EvVaultStart
	EvVaultEnd
)

// Event 是 sim 输出事件。
type Event struct {
	Kind EventKind
	A, B uint8
	Tick uint32
}

// ResultReason 是结算原因。
type ResultReason uint8

const (
	ReasonNone ResultReason = iota
	ReasonTimeUp
	ReasonEarlyWin
	// 加时（ReasonSuddenDeath）留待 room 层联调时按 D0001M05 占比差 < 1% 实现
)

// Result 是结算结果。
type Result struct {
	Ended  bool
	Reason ResultReason
	Ranks  [4]uint8 // playerId 1..4 → rank 0..3（0=冠军）
}

// Pallet 是板子状态（位置由 room 层按 A0001M09 注入）。
type Pallet struct {
	ID     uint8
	Pos    fixed.Vec2
	State  uint8 // 0=standing 1=down 2=broken
	Hits   int32 // 倒下后被攻击次数
	PushMS uint32
	By     uint8 // 推倒者
}

// Vault 是窗（位置由 room 层注入）。
type Vault struct {
	ID  uint8
	Pos fixed.Vec2
}

// Game 是一局游戏。
type Game struct {
	Players [4]Player
	Field   *territory.Field
	Pallets []Pallet
	Vaults  []Vault

	Tick      uint32
	DtMS      uint32 // 固定 1000/tickHz（50）
	ElapsedMS uint64

	AreaPermyriad [4]uint16
	Attr          [4]Attr
	Charging      [4]bool
	Result        Result

	dropTimerMS  uint32
	dropNextID   uint8
	dropFirstDone bool
	rng          *DetRand

	inputs [4]Input
	events []Event // 复用出站事件切片
}

// New 创建一局（field 由调用方 seed 好；seed 是确定性随机种子）。
func New(field *territory.Field, dtMS uint32, seed uint64) *Game {
	return &Game{Field: field, DtMS: dtMS, rng: NewDetRand(seed)}
}

// AddPlayer 登记玩家并放到出生点（坐标单位，room 层换算自 spawnX/Y）。
func (g *Game) AddPlayer(id uint8, pos fixed.Vec2) {
	if id < 1 || id > 4 {
		return
	}
	g.Players[id-1].Reset(id, pos)
}

// SetInput 由 room 层喂入本 tick 输入。
func (g *Game) SetInput(id uint8, in Input) {
	if id < 1 || id > 4 {
		return
	}
	g.inputs[id-1] = in
}

// Step 推进一 tick（T0004M04：复活 → 移动 → 交互 → 扩张 → 流失 → 战斗 → 道具 → 质量重算 → 结算）。
func (g *Game) Step() []Event {
	g.Tick++
	g.ElapsedMS += uint64(g.DtMS)
	g.events = g.events[:0]

	g.stepRespawn()
	g.stepMove()
	g.stepInteract()
	g.stepExpand()
	g.stepDissolve()
	g.stepCombat()
	g.stepItems()
	g.recomputeMass()
	g.stepEndgame()
	return g.events
}

func (g *Game) pushEv(k EventKind, a, b uint8) {
	g.events = append(g.events, Event{Kind: k, A: a, B: b, Tick: g.Tick})
}

// ---- 复活（D0001M07：3s→5s→8s 阶梯，1.5s 无敌） ----

func (g *Game) stepRespawn() {
	for i := range g.Players {
		p := &g.Players[i]
		if !p.Dead {
			continue
		}
		p.AliveMS += g.DtMS
		ladder := KNOBRespawnLadderMS
		idx := int(p.DeathCount) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(ladder) {
			idx = len(ladder) - 1
		}
		if p.AliveMS >= ladder[idx] {
			p.Dead = false
			p.HP = KNOB_HPMax
			p.InvulnMS = KNOBRespawnInvulnMS
			p.Vel = fixed.Vec2{}
			g.pushEv(EvPlayerRespawn, p.ID, 0)
		}
	}
}

// ---- 移动（D0001M02 移速 × 输入方向 × dt；充能 ×0.45；翻窗/死亡/前摇不可移动） ----

func (g *Game) stepMove() {
	for i := range g.Players {
		p := &g.Players[i]
		if p.Dead || p.Vaulting {
			p.Vel = fixed.Vec2{}
			continue
		}
		in := g.inputs[i]
		p.Aim = fixedAngle(in.Aim)
		speed := g.Attr[i].Speed
		if in.Buttons&BtnExpandBit != 0 {
			speed = speed.Mul(KNOBChargeMoveFactor) // ×0.45
		}
		if p.AttackWindupMS > 0 {
			p.Vel = fixed.Vec2{}
			continue // 前摇不可移动（递减在 stepCombat 单处）
		}
		// 方向（定点归一化）：输入 -100..100 → 定点
		dir := fixed.Vec2{X: fixed.I(int32(in.MoveX)), Y: fixed.I(int32(in.MoveY))}
		dir = dir.Normalized()
		disp := dir.Mul(speed.Mul(fixed.F(int32(g.DtMS))).Div(fixed.F(1000)))
		p.Vel = disp.Mul(fixed.I(20)) // 速度（单位/秒）= 位移 × 1000/dt（dt=50ms）
		p.Pos = p.Pos.Add(disp)
		clampWorld(&p.Pos)
	}
}

// ---- 交互：推板子 / 翻窗 ----

func (g *Game) stepInteract() {
	for i := range g.Players {
		p := &g.Players[i]
		if p.Dead || p.Vaulting || p.AttackWindupMS > 0 {
			continue
		}
		in := g.inputs[i]
		if in.Buttons&BtnInteractBit == 0 {
			continue
		}
		if g.tryVault(p) {
			continue
		}
		g.tryPushPallet(p)
	}
}

// tryVault 尝试翻最近的窗（D0001M03：耗时 = 属性表，期间不可移动/攻击/击退，可被攻击）。
func (g *Game) tryVault(p *Player) bool {
	if len(g.Vaults) == 0 {
		return false
	}
	best, bestD := -1, fixed.F(1)<<20
	for i := range g.Vaults {
		d := g.Vaults[i].Pos.Sub(p.Pos).Len2()
		if best < 0 || d < bestD {
			best, bestD = i, d
		}
	}
	// 交互范围 1.5 单位（与 KNOBPalletStunRange 同量级）
	if bestD > fixed.F(1536)*fixed.F(1536) {
		return false
	}
	p.Vaulting = true
	p.VaultMS = g.Attr[g.idx(p.ID)].VaultMS
	p.VaultID = g.Vaults[best].ID
	g.pushEv(EvVaultStart, p.ID, p.VaultID)
	return true
}

func (g *Game) tryPushPallet(p *Player) {
	if g.Attr[g.idx(p.ID)].Heavy {
		return // 过重不能下板子（D0001M04）
	}
	for i := range g.Pallets {
		pl := &g.Pallets[i]
		if pl.State != 0 || pl.Pos.Sub(p.Pos).Len2() > fixed.F(1536)*fixed.F(1536) {
			continue
		}
		pl.State = 1
		pl.PushMS = KNOBPalletPushMS
		pl.By = p.ID
		g.pushEv(EvPalletDown, pl.ID, p.ID)
		break
	}
}

// ---- 扩张（D0001M05：territory.Step，润甜 ×2.5） ----

func (g *Game) stepExpand() {
	var rates [4]fixed.F
	for i := range g.Players {
		p := &g.Players[i]
		g.Charging[i] = false
		if p.Dead || p.InvulnMS > 0 {
			continue
		}
		if g.inputs[i].Buttons&BtnExpandBit != 0 {
			r := KNOBExpandRate
			if p.ExpandMS > 0 {
				r = r.Mul(KNOBExpandBoost) // 润甜 ×2.5（D0001M08）
			}
			rates[i] = r
			g.Charging[i] = true
		}
	}
	g.Field.StepRates(rates, g.Tick)
}

// ---- 流失（D0001M07：死者地盘按 DissolveRateFor 半径回退） ----

func (g *Game) stepDissolve() {
	for i := range g.Players {
		p := &g.Players[i]
		if !p.Dead {
			continue
		}
		rate := DissolveRateFor(g.Field.Area(p.ID))
		g.Field.Dissolve(p.ID, rate, g.Tick)
	}
}

// ---- 战斗（D0001M06：15 × 倍率 × (1−减免)；前摇 0.15s 后出伤；范围 2.4 单位 90° 扇形） ----

func (g *Game) stepCombat() {
	for i := range g.Players {
		p := &g.Players[i]
		if p.Dead {
			continue
		}
		if p.AttackWindupMS > 0 {
			p.AttackWindupMS = subMS(p.AttackWindupMS, g.DtMS)
			if p.AttackWindupMS == 0 {
				g.applyHit(p, p.WindupTarget)
				p.AttackCDMS = KNOBAttackCDMS
			}
			continue
		}
		in := g.inputs[i]
		if in.Buttons&BtnAttackBit == 0 {
			continue
		}
		if p.AttackCDMS > 0 || g.Charging[i] || p.InvulnMS > 0 {
			continue // cd 中 / 充能不可攻击 / 无敌
		}
		// 开始前摇，锁定目标（前摇结束时判定）
		p.AttackWindupMS = KNOBAttackWindupMS
		p.WindupTarget = g.nearestInArc(i)
	}
}

// applyHit 造成一次命中（windup 结束）。
func (g *Game) applyHit(attacker *Player, targetID uint8) {
	if targetID == 0 {
		return
	}
	t := &g.Players[int(targetID)-1]
	if t.Dead || t.InvulnMS > 0 {
		return
	}
	// 位置判定（前摇锁定，命中瞬间复核距离与角度）
	if g.distArc(attacker, t) > KNOBAttackRange {
		return
	}
	at := g.Attr[g.idx(attacker.ID)]
	tt := g.Attr[g.idx(targetID)]
	dmg := fixed.I(KNOBAttackDamage).Mul(at.AtkMul)
	dmg = dmg.Mul(fixed.I(100).Sub(tt.DmgRed)).Div(fixed.I(100))
	if t.Shield > 0 {
		absorb := min32(t.Shield, int32(dmg.ToInt()))
		t.Shield -= absorb
		dmg = fixed.F(int32(dmg.ToInt()) - absorb)
	}
	t.HP -= int32(dmg.ToInt())
	if t.HP <= 0 {
		t.HP = 0
		t.Dead = true
		t.AliveMS = 0
		t.DeathCount++
		g.pushEv(EvPlayerDied, t.ID, attacker.ID)
	}
}

// ---- 道具（D0001M08：0:30 首刷、20s 间隔、锅心 12 单位内） ----

func (g *Game) stepItems() {
	for i := range g.Players {
		p := &g.Players[i]
		if p.ShieldMS > 0 {
			p.ShieldMS = subMS(p.ShieldMS, g.DtMS)
			if p.ShieldMS == 0 {
				p.Shield = 0
			}
		}
		if p.ExpandMS > 0 {
			p.ExpandMS = subMS(p.ExpandMS, g.DtMS)
		}
		if p.InvulnMS > 0 {
			p.InvulnMS = subMS(p.InvulnMS, g.DtMS)
		}
		if p.AttackCDMS > 0 {
			p.AttackCDMS = subMS(p.AttackCDMS, g.DtMS)
		}
		if p.Vaulting {
			p.VaultMS = subMS(p.VaultMS, g.DtMS)
			if p.VaultMS == 0 {
				p.Vaulting = false
				g.pushEv(EvVaultEnd, p.ID, 0)
			}
		}
		if p.Shield > 0 && p.ShieldMS == 0 {
			p.Shield = 0
		}
	}
	// 掉落生成（D0001M08：0:30 首刷，此后每 20s 一个）
	if !g.dropFirstDone && g.ElapsedMS >= KNOBDropFirstMS {
		g.dropFirstDone = true
		g.dropNextID++
		g.pushEv(EvDropSpawn, g.dropNextID, 0)
		g.dropTimerMS = 0
		return
	}
	if g.dropFirstDone {
		g.dropTimerMS += g.DtMS
		if g.dropTimerMS >= KNOBDropIntervalMS {
			g.dropTimerMS = 0
			g.dropNextID++
			g.pushEv(EvDropSpawn, g.dropNextID, 0)
		}
	}
}

// ---- 质量重算（territory.Ratios → 派生属性 LUT） ----

func (g *Game) recomputeMass() {
	ratios := g.Field.Ratios()
	for i := 0; i < 4; i++ {
		g.AreaPermyriad[i] = ratios[i]
		p := &g.Players[i]
		if p.Dead {
			continue
		}
		g.Attr[i] = p.EffectiveAttrs(ratios[i])
	}
}

// ---- 结算（D0001M05：3:00 / 65% 提前 / 加时） ----

func (g *Game) stepEndgame() {
	if g.Result.Ended {
		return
	}
	if g.Tick >= KNOBMatchDurationTick {
		g.finish(ReasonTimeUp)
		return
	}
	// 提前结束：任一人 ≥65%
	for i := 0; i < 4; i++ {
		if int(g.AreaPermyriad[i]) >= KNOBEarlyWinPermyriad {
			g.finish(ReasonEarlyWin)
			return
		}
	}
	// 加时（占比差 < 1% 触发）实现留待 room 层联调（需真实对局统计）。
}

func (g *Game) finish(reason ResultReason) {
	g.Result.Ended = true
	g.Result.Reason = reason
	// 排名按面积降序
	order := [4]int{0, 1, 2, 3}
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			if g.AreaPermyriad[order[j]] > g.AreaPermyriad[order[i]] {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	for rank, idx := range order {
		g.Result.Ranks[idx] = uint8(rank)
	}
}

// ---- 几何辅助 ----

// fixedAngle 把 u16 角度（0..65535 → 0..2π）转定点弧度（与 fixed.TwoPi 一致）。
func fixedAngle(a uint16) fixed.F {
	return fixed.F(int32(a)).Mul(fixed.TwoPi).Div(fixed.F(65536))
}

// nearestInArc 找攻击者 90° 扇形内最近的非己活玩家（0 = 无）。
func (g *Game) nearestInArc(idx int) uint8 {
	attacker := &g.Players[idx]
	best := uint8(0)
	bestD := fixed.F(1) << 20
	for i := range g.Players {
		t := &g.Players[i]
		if i == idx || t.Dead {
			continue
		}
		if g.distArc(attacker, t) < bestD {
			bestD = g.distArc(attacker, t)
			best = t.ID
		}
	}
	if bestD <= KNOBAttackRange {
		return best
	}
	return 0
}

// distArc 返回目标距离；不在 90° 扇形内返回超大值。
func (g *Game) distArc(a, t *Player) fixed.F {
	d := t.Pos.Sub(a.Pos)
	dist := d.Len()
	if dist == 0 {
		return 0
	}
	// 方向点积 ≥ cos45° ≈ 0.7071（dir、aimVec 均为单位向量）
	dir := d.Mul(fixed.F(1024).Div(dist))
	aimVec := fixed.Vec2{X: fixed.Cos(a.Aim), Y: fixed.Sin(a.Aim)}
	dot := dir.Dot(aimVec)
	if dot < fixed.F(724) { // 0.7071×1024 = 724
		return fixed.F(1) << 20
	}
	return dist
}

func (g *Game) idx(id uint8) int { return int(id) - 1 }

func subMS(v, dt uint32) uint32 {
	if v <= dt {
		return 0
	}
	return v - dt
}

func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

// clampWorld 把坐标夹到锅内（0..48 单位）。
func clampWorld(p *fixed.Vec2) {
	if p.X < 0 {
		p.X = 0
	}
	if p.Y < 0 {
		p.Y = 0
	}
	if p.X > fixed.F(48*1024) {
		p.X = fixed.F(48 * 1024)
	}
	if p.Y > fixed.F(48*1024) {
		p.Y = fixed.F(48 * 1024)
	}
}

// 输入位定义（与 T0001M02F04 一致，sim 独立声明避免 import proto）。
const (
	BtnAttackBit   uint8 = 1 << 0
	BtnExpandBit   uint8 = 1 << 1
	BtnInteractBit uint8 = 1 << 2
	BtnSkillBit    uint8 = 1 << 3
)
