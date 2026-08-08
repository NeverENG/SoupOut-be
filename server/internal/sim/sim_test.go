package sim

import (
	"math"
	"testing"

	"soupout-server/internal/territory"
	"soupout-server/pkg/fixed"
)

// ---- LUT（D0001M02 主属性表） ----

func TestAttrsAnchors(t *testing.T) {
	cases := []struct {
		permyriad  uint16
		speed      float64
		atk        float64
		dmgRed     float64
		vaultMS    uint32
		size       float64
		heavy      bool
	}{
		{500, 6.2, 0.80, 0, 350, 0.70, false},
		{1000, 6.0, 0.85, 0, 400, 0.80, false},
		{2000, 5.6, 0.95, 5, 650, 1.00, false},
		{3000, 5.2, 1.05, 10, 900, 1.20, false},
		{3500, 5.0, 1.10, 12, 1030, 1.30, true},
		{4000, 4.9, 1.15, 14, 1150, 1.40, true},
		{5000, 4.7, 1.24, 17, 1400, 1.60, true},
		{6500, 4.5, 1.35, 20, 1780, 1.85, true},
	}
	for _, c := range cases {
		at := AttrsFor(c.permyriad)
		if !closeF(at.Speed, c.speed) || !closeF(at.AtkMul, c.atk) ||
			at.DmgRed.ToInt() != int32(c.dmgRed) || at.VaultMS != c.vaultMS ||
			!closeF(at.Size, c.size) || at.Heavy != c.heavy {
			t.Errorf("AttrsFor(%d) = %+v, want speed=%.1f atk=%.2f red=%.0f vault=%d size=%.2f heavy=%v",
				c.permyriad, at, c.speed, c.atk, c.dmgRed, c.vaultMS, c.size, c.heavy)
		}
	}
}

func TestAttrsInterp(t *testing.T) {
	// 15% 应在 10% 与 20% 之间线性
	at := AttrsFor(1500)
	if !closeF(at.Speed, 5.8) { // (6.0+5.6)/2
		t.Errorf("15%% speed = %v, want 5.8", at.Speed.ToFloat())
	}
	if at.VaultMS != 525 { // 400 + (650-400)×0.5 = 525（修复 VaultMS 插值回归防线）
		t.Errorf("15%% vault = %d, want 525", at.VaultMS)
	}
	// 越界夹取
	if !closeF(AttrsFor(100).Speed, 6.2) {
		t.Error("clamp low failed")
	}
	if !closeF(AttrsFor(9000).Speed, 4.5) {
		t.Error("clamp high failed")
	}
}

// ---- 战斗伤害（D0001M06 校验：重打轻 20.3 / 轻打重 10.2 / 同体型 14.2） ----

func TestDamageMatrix(t *testing.T) {
	g := newDuelGame(t)
	// 面积直接注入派生属性（绕过 territory 扩张）
	setMass := func(id uint8, permyriad uint16) {
		g.Attr[id-1] = g.Players[id-1].EffectiveAttrs(permyriad)
	}
	_ = setMass

	// 重(65%) 打 轻(10%)：15 × 1.35 × 1.00 = 20.25 → HP 整数截断 20
	if dmg := calcDamage(g, 4, 6500, 1, 1000); dmg.ToInt() != 20 {
		t.Errorf("heavy→light dmg = %d, want 20", dmg.ToInt())
	}
	// 轻(10%) 打 重(65%)：15 × 0.85 × 0.80 = 10.2 → 10
	if dmg := calcDamage(g, 1, 1000, 4, 6500); dmg.ToInt() != 10 {
		t.Errorf("light→heavy dmg = %d, want 10", dmg.ToInt())
	}
	// 同体型(30%)：15 × 1.05 × 0.90 = 14.175 → 14
	if dmg := calcDamage(g, 2, 3000, 3, 3000); dmg.ToInt() != 14 {
		t.Errorf("same dmg = %d, want 14", dmg.ToInt())
	}
}

// calcDamage 模拟一次命中（目标放在攻击者 +X 方向 1 单位，通过距离/角度判定）。
func calcDamage(g *Game, attackerID uint8, atkMass uint16, targetID uint8, tgtMass uint16) fixed.F {
	attacker := &g.Players[attackerID-1]
	target := &g.Players[targetID-1]
	attacker.ID, target.ID = attackerID, targetID
	attacker.Pos = fixed.Vec2{X: fixed.I(10), Y: fixed.I(10)}
	target.Pos = fixed.Vec2{X: fixed.I(11), Y: fixed.I(10)} // +X 方向 1 单位
	attacker.Aim = 0
	g.Attr[attackerID-1] = attacker.EffectiveAttrs(atkMass)
	g.Attr[targetID-1] = target.EffectiveAttrs(tgtMass)
	target.HP = KNOB_HPMax
	target.Dead = false
	target.InvulnMS = 0
	g.applyHit(attacker, targetID)
	return fixed.I(KNOB_HPMax - target.HP)
}

// ---- 攻击流程：前摇 → 命中 → 死亡 → 流失 → 复活 ----

func TestAttackKillRespawn(t *testing.T) {
	g := newDuelGame(t)
	// A(1) 在 (10,10)，B(2) 在 (11,10)：距离 1.0 < 2.4
	a, b := &g.Players[0], &g.Players[1]
	a.Pos, b.Pos = fixed.Vec2{X: fixed.I(10), Y: fixed.I(10)}, fixed.Vec2{X: fixed.I(11), Y: fixed.I(10)}
	g.Attr[0] = AttrsFor(1000) // 同体型
	g.Attr[1] = AttrsFor(1000)
	a.Aim = 0 // 朝 +X（朝向 B）

	// 同体型(10%)：dmg = 15×0.85×(100-0)/100 = 12.75 → HP 整数截断 12/次。
	// 前摇 150ms + cd 450ms = 12 tick/轮；100 HP / 12 ≈ 9 次 → 108 tick。
	atkInput := Input{Buttons: BtnAttackBit, Has: true}
	noInput := Input{Has: false}
	g.SetInput(1, atkInput)
	g.SetInput(2, noInput)

	for tick := 0; tick < 150 && !b.Dead; tick++ {
		g.Step()
		// 攻击者 cd 结束后重按
		if a.AttackCDMS == 0 && !b.Dead {
			g.SetInput(1, atkInput)
		}
	}
	if !b.Dead {
		t.Fatal("target should be dead after ~9 hits")
	}
	if b.DeathCount != 1 {
		t.Fatalf("deathCount = %d", b.DeathCount)
	}
	// 死亡事件
	found := false
	for _, ev := range g.events {
		if ev.Kind == EvPlayerDied && ev.A == 2 && ev.B == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("no PlayerDied event")
	}

	// 流失：死亡后每 tick Dissolve（rate > 0）
	areaBefore := g.Field.Area(2)
	for i := 0; i < 10; i++ {
		g.SetInput(1, noInput)
		g.Step()
	}
	if g.Field.Area(2) >= areaBefore {
		t.Fatal("territory should dissolve while dead")
	}

	// 复活：3s = 60 tick
	for i := 0; i < 70 && b.Dead; i++ {
		g.SetInput(1, noInput)
		g.Step()
	}
	if b.Dead {
		t.Fatal("should respawn after ladder")
	}
	if b.InvulnMS == 0 {
		t.Fatal("respawn invuln should be active")
	}
}

// ---- 移动与充能减速 ----

func TestMoveSpeed(t *testing.T) {
	g := newGame(t)
	p := &g.Players[0]
	p.Pos = fixed.Vec2{X: fixed.I(10), Y: fixed.I(10)}
	g.Attr[0] = AttrsFor(1000) // 移速 6.0 单位/s
	// 朝 +X 满速 20 tick（1 秒）→ 6 单位
	in := Input{MoveX: 100, Has: true}
	for i := 0; i < 20; i++ {
		g.SetInput(1, in)
		g.Step()
	}
	got := p.Pos.X.Sub(fixed.I(10)).ToFloat()
	if math.Abs(got-6.0) > 0.3 {
		t.Errorf("move 1s = %v, want ≈6.0", got)
	}
	// 充能减速 ×0.45：6.0×0.45 = 2.7 单位/s
	p.Pos = fixed.Vec2{X: fixed.I(10), Y: fixed.I(10)}
	in.Buttons = BtnExpandBit
	for i := 0; i < 20; i++ {
		g.SetInput(1, in)
		g.Step()
	}
	got = p.Pos.X.Sub(fixed.I(10)).ToFloat()
	if math.Abs(got-2.7) > 0.3 {
		t.Errorf("charging move 1s = %v, want ≈2.7", got)
	}
}

// ---- 翻窗耗时 = LUT（轻 400ms / 重 1780ms） ----

func TestVaultDuration(t *testing.T) {
	if AttrsFor(1000).VaultMS != 400 {
		t.Errorf("light vault = %d, want 400", AttrsFor(1000).VaultMS)
	}
	if AttrsFor(6500).VaultMS != 1780 {
		t.Errorf("heavy vault = %d, want 1780", AttrsFor(6500).VaultMS)
	}
}

// ---- 溶解速率（D0001M07：r=30 格时 0.09 Chamfer/tick） ----

func TestDissolveRate(t *testing.T) {
	// A = π×30² ≈ 2827 格
	rate := DissolveRateFor(2827)
	if math.Abs(rate.ToFloat()-0.09) > 0.01 {
		t.Errorf("dissolve rate = %v, want ≈0.09", rate.ToFloat())
	}
}

// TestExpandBuff：润甜门控——充能且 buff 生效时速率 ×2.5，buff 结束后回落基础速率。
func TestExpandBuff(t *testing.T) {
	// 基线：无 buff 充能 300 tick 的面积
	base := func() int32 {
		f := territory.New(96, 96, territory.Circle{R: 48})
		f.Seed(1, territory.Cell(48*96+48), 400)
		g := New(f, 50, 1)
		g.AddPlayer(1, fixed.Vec2{X: fixed.I(10), Y: fixed.I(10)})
		g.Attr[0] = AttrsFor(1000)
		in := Input{Buttons: BtnExpandBit, Has: true}
		for i := 0; i < 300; i++ {
			g.SetInput(1, in)
			g.Step()
		}
		return g.Field.Area(1)
	}
	// buff 生效：同样 300 tick，前 120 tick 带润甜（×2.5）后回落
	buffed := func() int32 {
		f := territory.New(96, 96, territory.Circle{R: 48})
		f.Seed(1, territory.Cell(48*96+48), 400)
		g := New(f, 50, 1)
		g.AddPlayer(1, fixed.Vec2{X: fixed.I(10), Y: fixed.I(10)})
		g.Attr[0] = AttrsFor(1000)
		in := Input{Buttons: BtnExpandBit, Has: true}
		for i := 0; i < 300; i++ {
			if i == 60 {
				g.Players[0].ExpandMS = 6000 // 50ms/tick → 120 tick buff，从第 60 tick 起后半程生效
			}
			g.SetInput(1, in)
			g.Step()
		}
		return g.Field.Area(1)
	}
	if buffed() <= base() {
		t.Fatalf("expand buff should accelerate: base=%d buffed=%d", base(), buffed())
	}
	// 断言倍率生效：60 tick ×2.5 替换基础速率 → 面积显著大于基线（实测 +20% 左右）
	if buffed() <= base()*6/5 {
		t.Fatalf("buff too weak: base=%d buffed=%d", base(), buffed())
	}
}

// ---- 辅助 ----

func newGame(t *testing.T) *Game {
	t.Helper()
	f := territory.New(96, 96, territory.Circle{R: 48})
	f.Seed(1, territory.Cell(24*96+24), 724)
	g := New(f, 50, 42)
	g.AddPlayer(1, fixed.Vec2{X: fixed.I(10), Y: fixed.I(10)})
	return g
}

func newDuelGame(t *testing.T) *Game {
	t.Helper()
	f := territory.New(96, 96, territory.Circle{R: 48})
	f.Seed(1, territory.Cell(24*96+24), 724)
	f.Seed(2, territory.Cell(72*96+72), 724)
	g := New(f, 50, 42)
	g.AddPlayer(1, fixed.Vec2{X: fixed.I(10), Y: fixed.I(10)})
	g.AddPlayer(2, fixed.Vec2{X: fixed.I(20), Y: fixed.I(20)})
	// 开局派生属性（10%）
	g.Attr[0] = AttrsFor(1000)
	g.Attr[1] = AttrsFor(1000)
	return g
}

func closeF(a fixed.F, want float64) bool {
	return math.Abs(a.ToFloat()-want) < 0.02
}
