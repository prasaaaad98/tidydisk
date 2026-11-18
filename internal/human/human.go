// Package human formats and parses byte sizes for people.
package human

import (
	"fmt"
	"strconv"
	"strings"
)

// Bytes renders n as "1.5 GB" style text using 1024-based units.
func Bytes(n int64) string {
	if n < 0 {
		return "-" + Bytes(-n)
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	f := float64(n)
	i := -1
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// ParseSize reads sizes such as "500MB", "1.5 GB", "4096" (bytes).
func ParseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(t, u.suffix) {
			t = strings.TrimSuffix(t, u.suffix)
			mult = u.mult
			break
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q (try 500MB or 2GB)", s)
	}
	return int64(f * float64(mult)), nil
}
