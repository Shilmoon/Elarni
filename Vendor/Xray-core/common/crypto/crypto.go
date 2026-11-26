// Package crypto provides common crypto libraries for Xray.
package crypto // import "github.com/xtls/xray-core/common/crypto"

import (
	"crypto/rand"
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
	// to-from+1 in int64 would overflow for the widest ranges
	n := new(big.Int).Sub(big.NewInt(to), big.NewInt(from))
	bigInt, _ := rand.Int(rand.Reader, n.Add(n, big.NewInt(1)))
	return bigInt.Add(bigInt, big.NewInt(from)).Int64()
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
