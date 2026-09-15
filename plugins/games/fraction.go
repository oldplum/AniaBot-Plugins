// fraction.go 精确有理数运算：24 点游戏中除法会产生分数
// （如 8/(3-8/3)=24），用 int64 分数而非浮点判定，保证验证无误差。
package games

import (
	"errors"
	"strconv"
)

// frac 最简分数，den 恒为正。
type frac struct {
	num, den int64
}

var errZeroDen = errors.New("分母为零")

// newFrac 构造并约分。
func newFrac(num, den int64) (frac, error) {
	if den == 0 {
		return frac{}, errZeroDen
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd64(abs64(num), den)
	if g > 1 {
		num /= g
		den /= g
	}
	return frac{num, den}, nil
}

func (f frac) add(o frac) (frac, error) {
	return newFrac(f.num*o.den+o.num*f.den, f.den*o.den)
}

func (f frac) sub(o frac) (frac, error) {
	return newFrac(f.num*o.den-o.num*f.den, f.den*o.den)
}

func (f frac) mul(o frac) (frac, error) {
	return newFrac(f.num*o.num, f.den*o.den)
}

func (f frac) div(o frac) (frac, error) {
	if o.num == 0 {
		return frac{}, errZeroDen
	}
	return newFrac(f.num*o.den, f.den*o.num)
}

// isInt 判断是否等于整数 n。
func (f frac) isInt(n int64) bool {
	return f.den == 1 && f.num == n
}

func (f frac) String() string {
	if f.den == 1 {
		return strconv.FormatInt(f.num, 10)
	}
	return strconv.FormatInt(f.num, 10) + "/" + strconv.FormatInt(f.den, 10)
}

func gcd64(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
