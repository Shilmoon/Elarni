// Package crypto provides common crypto libraries for Xray.
package crypto // import "github.com/xtls/xray-core/common/crypto"

import (
	"crypto/rand"
	"encoding/binary"
	"math"
	"math/big"

	"github.com/xtls/xray-core/common"
)

// [,] Elarni: to is included, as every range of a config ("10-20") means it; it was left out, so a
// range of one step never took its end and "1-2" was always 1
func RandBetween(from int64, to int64) int64 {
	if from > to {
		from, to = to, from
	}
	if from == to {
		return from
	}
	// Elarni: the ranges of configs, which fragment, padding, noise and mux sizes draw from for every chunk or
	// request, take this path, which allocates nothing
	if d := to - from; d > 0 && d < math.MaxInt64 {
		return from + int64(randBelow(uint64(d)+1))
	}
	// to-from+1 in int64 would overflow for the widest ranges
	n := new(big.Int).Sub(big.NewInt(to), big.NewInt(from))
	bigInt, _ := rand.Int(rand.Reader, n.Add(n, big.NewInt(1)))
	return bigInt.Add(bigInt, big.NewInt(from)).Int64()
}

// randBelow returns a number in [0, n) that crypto/rand draws, each as likely as the others, for n > 0
func randBelow(n uint64) uint64 {
	// a draw at or above the largest multiple of n that fits in 64 bits is drawn again: it would make the
	// low numbers likelier
	limit := math.MaxUint64 - math.MaxUint64%n
	var b [8]byte
	for {
		// it never fails, and b does not escape
		_, _ = rand.Read(b[:])
		if v := binary.LittleEndian.Uint64(b[:]); v < limit {
			return v % n
		}
	}
}

// [,]
func RandBytesBetween(b []byte, from, to byte) {
	common.Must2(rand.Read(b))

	if from > to {
		from, to = to, from
	}

	if to-from == 255 {
		return
	}

	for i := range b {
		b[i] = from + b[i]%(to-from+1)
	}
}
