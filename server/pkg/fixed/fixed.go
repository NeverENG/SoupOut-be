// Package fixed 提供 Q22.10 定点数，供 sim 与 territory 使用（T0004M05：全线定点，不出现浮点）。
//
// 表示：int32 高 22 位整数、低 10 位小数。值域约 ±2048.999，精度 1/1024。
// 乘法/除法以 int64 作中间量防溢出，结果截断回 int32；调用方需保证业务数值
// 不超出 Q22.10 值域（本游戏 KNOB 最大约 100 质量 × 48 距离，见 D0001）。
package fixed

// F 是 Q22.10 定点数。
type F int32

const (
	// One 是定点数 1.0。
	One = F(1) << 10
	// FracBits 是小数位数。
	FracBits = 10
)

// I 由整数构造定点数。
func I(v int32) F { return F(v) << FracBits }

// FromFloat 由浮点构造定点数（仅测试与配置载入用，热路径禁止）。
func FromFloat(v float64) F { return F(v * float64(One)) }

// ToFloat 转浮点（仅测试与可视化用，热路径禁止）。
func (a F) ToFloat() float64 { return float64(a) / float64(One) }

// ToInt 向零截断取整。
func (a F) ToInt() int32 { return int32(a) >> FracBits }

// Floor 向下取整（整数边界返回自身）。
func (a F) Floor() int32 {
	i := a.ToInt()
	if a < F(i)<<FracBits {
		return i - 1
	}
	return i
}

// Ceil 向上取整（整数边界返回自身）。
func (a F) Ceil() int32 {
	i := a.ToInt()
	if a > F(i)<<FracBits {
		return i + 1
	}
	return i
}

func (a F) Add(b F) F { return a + b }
func (a F) Sub(b F) F { return a - b }
func (a F) Neg() F    { return -a }

// Abs 取绝对值。
func (a F) Abs() F {
	if a < 0 {
		return -a
	}
	return a
}

// Mul 定点乘法：Q22.10 × Q22.10 → Q44.20，右移 10 位截断回 Q22.10。
func (a F) Mul(b F) F { return F((int64(a) * int64(b)) >> FracBits) }

// Div 定点除法：Q22.10 ÷ Q22.10，被除数左移 10 位 → Q44.20 后除。
// b == 0 时结果为 0（调用方保证分母非零）。
func (a F) Div(b F) F {
	if b == 0 {
		return 0
	}
	return F((int64(a) << FracBits) / int64(b))
}

// Sqrt 整数牛顿迭代求平方根（Q22.10）。非热路径，迭代次数取足保证大数收敛。
func (a F) Sqrt() F {
	if a <= 0 {
		return 0
	}
	x := a
	for i := 0; i < 24; i++ {
		next := (x + a.Div(x)) / 2
		if next == x {
			break
		}
		x = next
	}
	return x
}

func Min(a, b F) F {
	if a < b {
		return a
	}
	return b
}

func Max(a, b F) F {
	if a > b {
		return a
	}
	return b
}

// Vec2 定点二维向量。
type Vec2 struct {
	X, Y F
}

func (v Vec2) Add(o Vec2) Vec2 { return Vec2{v.X + o.X, v.Y + o.Y} }
func (v Vec2) Sub(o Vec2) Vec2 { return Vec2{v.X - o.X, v.Y - o.Y} }
func (v Vec2) Mul(s F) Vec2    { return Vec2{v.X.Mul(s), v.Y.Mul(s)} }

// Dot 点积。
func (v Vec2) Dot(o Vec2) F { return v.X.Mul(o.X) + v.Y.Mul(o.Y) }

// Len2 距离平方（避免开方）。
func (v Vec2) Len2() F { return v.X.Mul(v.X) + v.Y.Mul(v.Y) }

// Len 欧氏距离。
func (v Vec2) Len() F { return v.Len2().Sqrt() }

// Normalized 单位向量；零向量返回自身。
func (v Vec2) Normalized() Vec2 {
	l := v.Len()
	if l == 0 {
		return v
	}
	return Vec2{v.X.Div(l), v.Y.Div(l)}
}
