// Package chunker implements Rabin-fingerprint content-defined chunking
// with the same parameters and algorithm as the official restic chunker, so
// dedup behavior and chunk statistics match upstream (chunk boundaries need
// not match byte-for-byte for compatibility, but the properties do).
//
// Algorithm ported from github.com/restic/chunker v0.5.0 (MIT, commit
// 2e8f53f), rewritten in this package's style. Verified against that source
// in docs/superpowers/notes/restic-format-verification.md §2.10.
package chunker

import "io"

const (
	kiB = 1024
	miB = 1024 * kiB

	windowSize = 64 // sliding window in bytes

	// MinSize is the minimum chunk size.
	MinSize = 512 * kiB
	// MaxSize is the maximum chunk size.
	MaxSize = 8 * miB

	chunkerBufSize = 512 * kiB

	// splitMask aims for chunks of 20 bits, ~1 MiB on average.
	splitMask = (1 << 20) - 1
)

// tables holds the two precomputed lookup tables for one polynomial.
type tables struct {
	out [256]Pol // contribution of the byte sliding out of the window
	mod [256]Pol // reduction table for the 8 bits above the polynomial degree
}

// Chunker splits a byte stream into content-dependent chunks.
type Chunker struct {
	rd       io.Reader
	pol      Pol
	polShift uint
	tab      tables
	minSize  uint
	maxSize  uint

	// rolling state
	window [windowSize]byte
	wpos   uint
	digest uint64
	pre    uint // bytes to skip before hunting the next split point
	count  uint // bytes in the current chunk

	// buffered reader state
	buf    []byte
	bpos   uint
	bmax   uint
	pos    uint
	closed bool
}

// New returns a Chunker reading from rd and using polynomial pol.
func New(rd io.Reader, pol Pol) *Chunker {
	c := &Chunker{
		rd:      rd,
		pol:     pol,
		minSize: MinSize,
		maxSize: MaxSize,
		buf:     make([]byte, chunkerBufSize),
	}
	c.reset()
	return c
}

// reset prepares the rolling state for a new chunk.
func (c *Chunker) reset() {
	c.polShift = uint(c.pol.Deg() - 8)
	c.fillTables()
	for i := 0; i < windowSize; i++ {
		c.window[i] = 0
	}
	c.digest = 0
	c.wpos = 0
	c.count = 0
	c.digest = c.slide(c.digest, 1)
	// do not start a new chunk unless at least MinSize bytes have been read
	c.pre = c.minSize - windowSize
}

// fillTables precomputes out and mod for the polynomial.
func (c *Chunker) fillTables() {
	// out[b] = H(b || 0 || ... || 0) with windowSize-1 zero bytes, so one
	// XOR removes the sliding-out byte from the fingerprint.
	for b := 0; b < 256; b++ {
		h := appendByte(0, byte(b), c.pol)
		for i := 0; i < windowSize-1; i++ {
			h = appendByte(h, 0, c.pol)
		}
		c.tab.out[b] = h
	}

	// mod[b] = (b(x) * x^k mod pol) | (b << k): one XOR reduces the 8 bits
	// pushed above the polynomial degree.
	k := uint(c.pol.Deg())
	for b := 0; b < 256; b++ {
		c.tab.mod[b] = Pol(uint64(b)<<k).Mod(c.pol) | Pol(b)<<k
	}
}

// slide pushes one byte into the fingerprint and slides the window.
func (c *Chunker) slide(digest uint64, b byte) uint64 {
	out := c.window[c.wpos]
	c.window[c.wpos] = b
	digest ^= uint64(c.tab.out[out])
	c.wpos = (c.wpos + 1) % windowSize
	return updateDigest(digest, c.polShift, &c.tab, b)
}

// updateDigest shifts in one byte and reduces modulo the polynomial.
func updateDigest(digest uint64, polShift uint, tab *tables, b byte) uint64 {
	index := digest >> polShift
	digest <<= 8
	digest |= uint64(b)
	digest ^= uint64(tab.mod[index])
	return digest
}

// appendByte shifts one byte into the polynomial hash and reduces.
func appendByte(hash Pol, b byte, pol Pol) Pol {
	hash <<= 8
	hash |= Pol(b)
	return hash.Mod(pol)
}

// nextSplitPoint scans buf for a chunk boundary and returns the index before
// which to split, or -1 if no boundary occurs in this buffer. Stateful: all
// buffers until a split point forms a single chunk.
func (c *Chunker) nextSplitPoint(buf []byte) (int, uint64) {
	if c.polShift > 53-8 {
		panic("chunker: the polynomial must have a degree less than or equal 53")
	}
	minSize := c.minSize
	maxSize := c.maxSize

	idx := 0
	if c.pre > 0 {
		if c.pre >= uint(len(buf)) {
			c.pre -= uint(len(buf))
			c.count += uint(len(buf))
			return -1, 0
		}
		buf = buf[c.pre:]
		idx = int(c.pre)
		c.count += c.pre
		c.pre = 0
	}

	add := c.count
	digest := c.digest
	win := c.window
	wpos := c.wpos
	for i, b := range buf {
		out := win[wpos%windowSize]
		win[wpos%windowSize] = b
		digest ^= uint64(c.tab.out[out])
		wpos++
		digest = updateDigest(digest, c.polShift, &c.tab, b)
		add++

		if (digest&splitMask) == 0 || add >= maxSize {
			if add < minSize {
				continue
			}
			c.reset()
			return idx + i + 1, digest
		}
	}
	c.digest = digest
	c.window = win
	c.wpos = wpos % windowSize
	c.count += uint(len(buf))
	return -1, 0
}

// Chunk is one content-dependent chunk.
type Chunk struct {
	Start  uint
	Length uint
	Cut    uint64
	Data   []byte
}

// Next returns the next chunk. The returned slice aliases the caller's data
// buffer (restic-style zero-copy API): pass the same buffer each call. After
// the last chunk, subsequent calls return io.EOF.
func (c *Chunker) Next(data []byte) (Chunk, error) {
	data = data[:0]
	start := c.pos
	for {
		if c.bpos >= c.bmax {
			n, err := io.ReadFull(c.rd, c.buf)
			if err == io.ErrUnexpectedEOF {
				err = nil
			}
			if err == io.EOF && !c.closed {
				c.closed = true
				if len(data) > 0 {
					return Chunk{Start: start, Length: uint(len(data)), Cut: c.digest, Data: data}, nil
				}
			}
			if err != nil {
				return Chunk{}, err
			}
			c.bpos = 0
			c.bmax = uint(n)
		}

		split, cut := c.nextSplitPoint(c.buf[c.bpos:c.bmax])
		if split == -1 {
			data = append(data, c.buf[c.bpos:c.bmax]...)
			c.pos += c.bmax - c.bpos
			c.bpos = c.bmax
		} else {
			data = append(data, c.buf[c.bpos:c.bpos+uint(split)]...)
			c.bpos += uint(split)
			c.pos += uint(split)
			return Chunk{Start: start, Length: uint(len(data)), Cut: cut, Data: data}, nil
		}
	}
}
