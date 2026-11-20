package billing

import (
	"math"
	"testing"
)

func TestCheckedFixedPointArithmetic(t *testing.T) {
	for _, factor := range []int64{1, 100, 150, 1000} {
		units, err := Units(1<<30, factor)
		if err != nil || units/100 != (1<<30)*factor/100 {
			t.Fatal("incorrect fixed point", factor, units, err)
		}
	}
	for _, value := range [][2]int64{{-1, 100}, {1, 0}, {1, 1001}, {math.MaxInt64, 2}} {
		if _, err := Units(value[0], value[1]); err == nil {
			t.Fatal("invalid or overflow accepted", value)
		}
	}
	if _, err := Add(math.MaxInt64, 1); err == nil {
		t.Fatal("addition overflow accepted")
	}
	if Ceiling(math.MaxInt64) != math.MaxInt64/100+1 {
		t.Fatal("rounding overflow")
	}
}
