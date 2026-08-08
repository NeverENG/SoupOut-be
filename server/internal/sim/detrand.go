package sim

// DetRand 是确定性随机（splitmix64），由建房 seed 播种。
// 独立于 SDK 的 DetRand（sim 保持零 SDK 依赖，room 层用同一 seed 播种两处，
// 各自序列确定；游戏逻辑只消费本实现，回放一致性成立）。
type DetRand struct {
	state uint64
}

// NewDetRand 用 seed 播种。
func NewDetRand(seed uint64) *DetRand { return &DetRand{state: seed} }

// Uint64 下一个 64 位随机数。
func (r *DetRand) Uint64() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Uint32 下一个 32 位随机数。
func (r *DetRand) Uint32() uint32 { return uint32(r.Uint64()) }

// IntN [0, n)；n ≤ 0 返回 0。
func (r *DetRand) IntN(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.Uint64() % uint64(n))
}

// Float01 [0,1) 的 Q22.10 定点值。
func (r *DetRand) Float01() int32 { return int32(r.Uint64() >> 54) } // 0..1023
