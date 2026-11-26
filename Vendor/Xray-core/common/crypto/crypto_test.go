package crypto_test

import (
	"math"
	"testing"

	"github.com/xtls/xray-core/common/crypto"
)

// A range of a config includes its end: RandBetween left it out, so "1-2" was always 1 and the largest
// size or delay of a range never came.
func TestRandBetweenIncludesBothEnds(t *testing.T) {
	for _, r := range [][2]int64{{0, 1}, {1, 2}, {5, 7}, {-3, -1}, {7, 5}} {
		low, high := min(r[0], r[1]), max(r[0], r[1])
		seen := make(map[int64]bool)
		for range 2000 {
			v := crypto.RandBetween(r[0], r[1])
			if v < low || v > high {
				t.Fatalf("RandBetween(%d, %d) = %d", r[0], r[1], v)
			}
			seen[v] = true
		}
		for v := low; v <= high; v++ {
			if !seen[v] {
				t.Errorf("RandBetween(%d, %d) never gave %d in 2000 draws", r[0], r[1], v)
			}
		}
	}
	if v := crypto.RandBetween(4, 4); v != 4 {
		t.Errorf("RandBetween(4, 4) = %d", v)
	}
	// the widest ranges do not overflow, nor do the widest that the path without big.Int takes
	for _, r := range [][2]int64{{0, math.MaxInt64}, {math.MinInt64, math.MaxInt64}, {math.MinInt64, 0},
		{1, math.MaxInt64}, {math.MinInt64, -1}} {
		if v := crypto.RandBetween(r[0], r[1]); v < r[0] || v > r[1] {
			t.Errorf("RandBetween(%d, %d) = %d", r[0], r[1], v)
		}
	}
}
