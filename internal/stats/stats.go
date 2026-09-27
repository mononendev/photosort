// Package stats reproduces the few numpy reductions photosort reports numbers from (percentiles for calibration and
// job timings, the candidate grid for threshold suggestions, means), bit for bit: the same float operations in the
// same order as numpy 2.x, so a Go result prints the same digits the Python one did.
//
// Every a*b+c below is written with an explicit float64() conversion around the product: Go may otherwise fuse it
// into one FMA instruction on arm64, which rounds once instead of twice and drifts from numpy in the last bit.
package stats

import (
	"math"
	"sort"
)

// Linspace is numpy.linspace(start, stop, num) (endpoint=True): start + i*step with step = (stop-start)/(num-1),
// the last value set to stop exactly.
func Linspace(start, stop float64, num int) []float64 {
	if num <= 0 {
		return []float64{}
	}
	out := make([]float64, num)
	div := num - 1
	delta := stop - start
	if div > 0 {
		step := delta / float64(div)
		for i := range out {
			if step == 0 {
				out[i] = float64(float64(i)/float64(div)*delta) + start
			} else {
				out[i] = float64(float64(i)*step) + start
			}
		}
	} else {
		// numpy: step is nan (0/0 delta) but only i=0 exists: 0*delta + start
		out[0] = float64(0*delta) + start
	}
	if num > 1 {
		out[num-1] = stop
	}
	return out
}

// Quantile is numpy.quantile(x, q) with the default "linear" method, for q in [0, 1]. x need not be sorted. NaN in x
// makes the result NaN, as in numpy. Quantile of an empty slice is NaN (numpy raises).
func Quantile(x []float64, q float64) float64 {
	return Quantiles(x, []float64{q})[0]
}

// Quantiles is Quantile for several q, sorting x once.
func Quantiles(x []float64, qs []float64) []float64 {
	out := make([]float64, len(qs))
	n := len(x)
	s := sorted(x)
	hasNaN := n > 0 && math.IsNaN(s[n-1])
	for i, q := range qs {
		if n == 0 || hasNaN {
			out[i] = math.NaN()
			continue
		}
		out[i] = quantileSorted(s, q)
	}
	return out
}

// quantileSorted follows numpy's _quantile: virtual index (n-1)*q, the neighbours below and above it (both clamped
// to the last element at or past the end, to the first below 0), then _lerp with gamma = index - below.
func quantileSorted(s []float64, q float64) float64 {
	n := len(s)
	vi := float64(float64(n-1) * q) // rounded here, or vi - prev below may fuse with the product
	prev := math.Floor(vi)
	next := prev + 1
	switch {
	case math.IsNaN(vi):
		prev, next = -1, -1
	case vi >= float64(n-1):
		prev, next = -1, -1
	case vi < 0:
		prev, next = 0, 0
	}
	gamma := vi - prev
	at := func(i float64) float64 {
		k := int(i)
		if k < 0 {
			k += n
		}
		return s[k]
	}
	return lerp(at(prev), at(next), gamma)
}

// lerp is numpy's _lerp: a + (b-a)*t, or b - (b-a)*(1-t) where t >= 0.5 (keeps the result monotonic in t).
func lerp(a, b, t float64) float64 {
	d := b - a
	if t >= 0.5 {
		return b - float64(d*(1-t))
	}
	return a + float64(d*t)
}

// Percentile is numpy.percentile(x, p) for p in [0, 100]: Quantile at p/100.
func Percentile(x []float64, p float64) float64 {
	return Quantile(x, p/100)
}

// Percentiles is Percentile for several p, sorting x once.
func Percentiles(x []float64, ps []float64) []float64 {
	qs := make([]float64, len(ps))
	for i, p := range ps {
		qs[i] = p / 100
	}
	return Quantiles(x, qs)
}

// Sum is numpy.sum(x) for a float64 array: pairwise summation in numpy's blocks (sequential under 8 values, eight
// running sums up to 128, halves above), which is not the same as adding left to right.
func Sum(x []float64) float64 {
	n := len(x)
	switch {
	case n < 8:
		res := 0.0
		for _, v := range x {
			res += v
		}
		return res
	case n <= 128:
		var r [8]float64
		copy(r[:], x[:8])
		i := 8
		for ; i < n-n%8; i += 8 {
			for j := range r {
				r[j] += x[i+j]
			}
		}
		res := ((r[0] + r[1]) + (r[2] + r[3])) + ((r[4] + r[5]) + (r[6] + r[7]))
		for ; i < n; i++ {
			res += x[i]
		}
		return res
	default:
		n2 := n / 2
		n2 -= n2 % 8
		return Sum(x[:n2]) + Sum(x[n2:])
	}
}

// Mean is numpy.mean(x): Sum / len. NaN for an empty slice (numpy warns and returns nan).
func Mean(x []float64) float64 {
	return Sum(x) / float64(len(x))
}

// Unique is numpy.unique(x): the sorted distinct values (NaNs, sorted last, collapse into one).
func Unique(x []float64) []float64 {
	s := sorted(x)
	out := make([]float64, 0, len(s))
	for i, v := range s {
		if i > 0 && (v == s[i-1] || (math.IsNaN(v) && math.IsNaN(s[i-1]))) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// Argmax is numpy.argmax(x): the index of the first maximum, or of the first NaN if there is one; -1 when empty.
func Argmax(x []float64) int {
	best := -1
	for i, v := range x {
		if math.IsNaN(v) {
			return i
		}
		if best < 0 || v > x[best] {
			best = i
		}
	}
	return best
}

// sorted copies and sorts x ascending with NaNs last (numpy's order).
func sorted(x []float64) []float64 {
	s := append([]float64(nil), x...)
	sort.Slice(s, func(i, j int) bool {
		a, b := s[i], s[j]
		if math.IsNaN(b) {
			return !math.IsNaN(a)
		}
		return a < b
	})
	return s
}
