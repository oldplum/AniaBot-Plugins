// money.go 金额的解析与格式化：以「分」为单位记账，避免浮点误差。
// 纯函数实现，便于表驱动测试。
package ledger

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var amountRe = regexp.MustCompile(`^(\d{1,9})(?:\.(\d{1,2}))?$`)

// parseAmount 解析金额字符串（单位：元），返回（分，是否收入）。
//
// 支持可选的正负号：无符号默认支出，+ 为收入，- 为支出。
// 小数最多两位；整数部分最多 9 位。例如：25 / 25.5 / +3000 / -12.75。
func parseAmount(s string) (int64, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, fmt.Errorf("金额不能为空")
	}

	income := false
	switch s[0] {
	case '+':
		income = true
		s = s[1:]
	case '-':
		income = false
		s = s[1:]
	default:
		income = false // 无符号默认支出
	}

	m := amountRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false, fmt.Errorf("金额格式不对：%s（示例：25 / 25.5 / +3000）", s)
	}
	yuan, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("金额太大")
	}
	cents := yuan * 100
	if m[2] != "" {
		// 小数部分右补齐两位（25.5 → 50 分）
		frac := m[2] + "00"
		c, err := strconv.Atoi(frac[:2])
		if err != nil {
			return 0, false, fmt.Errorf("金额格式不对：%s", s)
		}
		cents += int64(c)
	}
	if income {
		return cents, true, nil
	}
	return -cents, false, nil
}

// formatCents 把分格式化为元的字符串，保留两位小数，带正负号。
func formatCents(c int64) string {
	sign := ""
	if c < 0 {
		sign = "-"
		c = -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}
