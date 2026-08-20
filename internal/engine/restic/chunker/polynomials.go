package chunker

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/bits"
	"strconv"
)

// Pol is a polynomial over F_2, represented by its coefficients as bits.
// Ported from github.com/restic/chunker v0.5.0 (MIT, commit 2e8f53f).
type Pol uint64

// Add returns x+y (addition in F_2[X] is XOR).
func (x Pol) Add(y Pol) Pol { return x ^ y }

// Deg returns the degree of the polynomial; -1 for the zero polynomial.
func (x Pol) Deg() int { return bits.Len64(uint64(x)) - 1 }

// DivMod returns the quotient q and remainder r of x / d.
func (x Pol) DivMod(d Pol) (Pol, Pol) {
	if x == 0 {
		return 0, 0
	}
	if d == 0 {
		panic("chunker: division by zero polynomial")
	}
	D := d.Deg()
	diff := x.Deg() - D
	if diff < 0 {
		return 0, x
	}
	var q Pol
	for diff >= 0 {
		m := d << uint(diff)
		q |= 1 << uint(diff)
		x = x.Add(m)
		diff = x.Deg() - D
	}
	return q, x
}

// Mod returns the remainder of x / d.
func (x Pol) Mod(d Pol) Pol {
	_, r := x.DivMod(d)
	return r
}

// Div returns the integer division result x / d.
func (x Pol) Div(d Pol) Pol {
	q, _ := x.DivMod(d)
	return q
}

// Mul returns x*y, panicking on uint64 overflow (as upstream does).
func (x Pol) Mul(y Pol) Pol {
	switch {
	case x == 0 || y == 0:
		return 0
	case x == 1:
		return y
	case y == 1:
		return x
	case y == 2:
		if x&(1<<63) != 0 {
			panic("chunker: multiplication would overflow uint64")
		}
		return x << 1
	}
	var res Pol
	for i := 0; i <= y.Deg(); i++ {
		if y&(1<<uint(i)) > 0 {
			res = res.Add(x << uint(i))
		}
	}
	if res.Div(y) != x {
		panic("chunker: multiplication would overflow uint64")
	}
	return res
}

// MulMod computes x*f mod g.
func (x Pol) MulMod(f, g Pol) Pol {
	if x == 0 || f == 0 {
		return 0
	}
	var res Pol
	for i := 0; i <= f.Deg(); i++ {
		if f&(1<<uint(i)) > 0 {
			a := x
			for j := 0; j < i; j++ {
				a = a.Mul(2).Mod(g)
			}
			res = res.Add(a).Mod(g)
		}
	}
	return res
}

// GCD computes the greatest common divisor of x and f.
func (x Pol) GCD(f Pol) Pol {
	if f == 0 {
		return x
	}
	if x == 0 {
		return f
	}
	if x.Deg() < f.Deg() {
		x, f = f, x
	}
	return f.GCD(x.Mod(f))
}

// qp computes (x^(2^p) - x) mod g, the core of Ben-Or's reducibility test.
func qp(p uint, g Pol) Pol {
	num := 1 << p
	i := 1
	res := Pol(2) // start with x
	for i < num {
		res = res.MulMod(res, g)
		i *= 2
	}
	return res.Add(2).Mod(g)
}

// Irreducible reports whether x is irreducible over F_2 (Ben-Or test).
func (x Pol) Irreducible() bool {
	for i := 1; i <= x.Deg()/2; i++ {
		if x.GCD(qp(uint(i), x)) != 1 {
			return false
		}
	}
	return true
}

// randPolMaxTries bounds the search; irreducibles are dense so this is never
// hit in practice, but a non-terminating loop is worse than an error.
const randPolMaxTries = 1e6

// RandomPolynomial returns a random irreducible polynomial of degree 53
// using crypto/rand.
func RandomPolynomial() (Pol, error) {
	return DerivePolynomial(rand.Reader)
}

// DerivePolynomial reads random bytes from source and returns an
// irreducible polynomial of degree 53 (the largest prime below 64-8, the
// bound required by the sliding-window reduction). Bits above 53 are
// masked, bits 53 and 0 are set.
func DerivePolynomial(source io.Reader) (Pol, error) {
	for i := 0; i < randPolMaxTries; i++ {
		var f Pol
		if err := binary.Read(source, binary.LittleEndian, &f); err != nil {
			return 0, err
		}
		f &= (1 << 54) - 1 // mask bits above 53
		f |= (1 << 53) | 1 // degree 53, not trivially reducible
		if f.Irreducible() {
			return f, nil
		}
	}
	return 0, errors.New("chunker: unable to find a random irreducible polynomial")
}

// MarshalJSON serializes the polynomial as a hex string, matching the
// chunker_polynomial field in the restic repository config.
func (x Pol) MarshalJSON() ([]byte, error) {
	buf := strconv.AppendUint([]byte{'"'}, uint64(x), 16)
	buf = append(buf, '"')
	return buf, nil
}

// UnmarshalJSON parses a hex string into a polynomial.
func (x *Pol) UnmarshalJSON(data []byte) error {
	if len(data) < 2 {
		return errors.New("chunker: invalid polynomial string")
	}
	n, err := strconv.ParseUint(string(data[1:len(data)-1]), 16, 64)
	if err != nil {
		return err
	}
	*x = Pol(n)
	return nil
}

// ensure json.Marshaler is satisfied via the exported methods above.
var _ json.Marshaler = Pol(0)
