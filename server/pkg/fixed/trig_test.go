package fixed

import "testing"

func TestSinCos(t *testing.T) {
	halfPi := TwoPi / 4 // π/2
	pi := TwoPi / 2     // π
	cases := []struct {
		name string
		got  F
		want float64
	}{
		{"sin0", Sin(0), 0},
		{"sin_pi2", Sin(halfPi), 1},
		{"sin_pi", Sin(pi), 0},
		{"sin_3pi2", Sin(halfPi * 3), -1},
		{"sin_2pi", Sin(TwoPi), 0},
		{"cos0", Cos(0), 1},
		{"cos_pi2", Cos(halfPi), 0},
		{"cos_pi", Cos(pi), -1},
		{"sin_negative", Sin(-halfPi), -1},
	}
	for _, c := range cases {
		if !trigClose(c.got.ToFloat(), c.want) {
			t.Errorf("%s = %v, want ≈%v", c.name, c.got.ToFloat(), c.want)
		}
	}
	// 恒等式 sin²+cos² ≈ 1
	a := halfPi / 3 * 2
	s, c := Sin(a), Cos(a)
	got := s.Mul(s).Add(c.Mul(c)).ToFloat()
	if !trigClose(got, 1) {
		t.Errorf("sin²+cos² = %v, want ≈1", got)
	}
}

func trigClose(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 0.03 // LUT 分段线性精度
}
