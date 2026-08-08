package fixed

// 三角函数：256 项 sin LUT + 线性插值（Q22.10 弧度输入，Q22.10 输出）。
// 运行期零浮点；表在包初始化时用泰勒级数构建（构建期浮点可接受）。
// 角度为弧度 [0, 2π)；超出回绕。精度 ≈ 2π/256 ≈ 0.0245 rad 分段线性。

const (
	// TwoPi 是 2π 的 Q22.10 近似（6.283185 × 1024 = 6433.98 → 6434）。
	TwoPi = F(6434)

	sinTableSize = 256
)

var sinTable [sinTableSize + 1]F

func init() {
	for i := 0; i <= sinTableSize; i++ {
		x := float64(i) / sinTableSize * 6.283185307179586
		sinTable[i] = F(int32(sinTaylor(x) * 1024.0))
	}
}

// sinTaylor 构建期 sin（泰勒级数，仅 init 用）。
func sinTaylor(x float64) float64 {
	// 归约到 [-π, π]
	for x > 3.141592653589793 {
		x -= 6.283185307179586
	}
	for x < -3.141592653589793 {
		x += 6.283185307179586
	}
	term, sum := x, x
	for i := 1; i < 12; i++ {
		term = -term * x * x / float64((2*i)*(2*i+1))
		sum += term
	}
	return sum
}

// Sin 返回 sin(a)（Q22.10）。
func Sin(a F) F {
	x := int64(a) % int64(TwoPi)
	if x < 0 {
		x += int64(TwoPi)
	}
	idx := int(x * sinTableSize / int64(TwoPi))
	frac := F(x*sinTableSize%int64(TwoPi)) / TwoPi
	return sinTable[idx].Add(sinTable[idx+1].Sub(sinTable[idx]).Mul(frac))
}

// Cos 返回 cos(a)（Q22.10）：cos(a) = sin(a + π/2)。
func Cos(a F) F { return Sin(a + TwoPi/4) }
