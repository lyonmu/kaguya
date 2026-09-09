package system

import "errors"

// MaxUnixSecond 为 9999-12-31T23:59:59Z；限制有效日期范围并拒绝现代毫秒时间戳。
const MaxUnixSecond int64 = 253402300799

var ErrInvalidTimeRange = errors.New("invalid Unix-second time range")

// ValidateTimeRange 时间筛选统一使用 Unix 秒；0 表示未指定，起止秒均包含。
func ValidateTimeRange(start, end int64) error {
	if start < 0 || end < 0 || start > MaxUnixSecond || end > MaxUnixSecond || (start > 0 && end > 0 && start > end) {
		return ErrInvalidTimeRange
	}
	return nil
}
