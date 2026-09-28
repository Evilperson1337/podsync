package audiosig

import (
	"math"
	"math/bits"
	"math/cmplx"
)

// fftMinPatternLength is the pattern length from which CorrelateNormalized switches from the
// direct O(n*m) loop to FFT. Short patterns (coarse envelopes) are cheaper and exact directly;
// long ones (PCM refine windows) are orders of magnitude faster with FFT.
const fftMinPatternLength = 256

// correlateNormalizedFFT computes the same scores as the direct loop in CorrelateNormalized,
// using FFT for the cross-correlation and prefix sums for per-window signal energy.
func correlateNormalizedFFT(signal []float64, pattern []float64, patEnergy float64) []float64 {
	n, m := len(signal), len(pattern)
	size := 1 << bits.Len(uint(n-1))

	sigSpec := make([]complex128, size)
	for i, v := range signal {
		sigSpec[i] = complex(v, 0)
	}
	patSpec := make([]complex128, size)
	for i, v := range pattern {
		patSpec[i] = complex(v, 0)
	}
	fft(sigSpec, false)
	fft(patSpec, false)
	for i := range sigSpec {
		sigSpec[i] *= cmplx.Conj(patSpec[i])
	}
	fft(sigSpec, true)

	// prefix[i] is the energy of signal[:i].
	prefix := make([]float64, n+1)
	for i, v := range signal {
		prefix[i+1] = prefix[i] + v*v
	}
	// Windows far quieter than average carry only FFT rounding noise; score them as zero like
	// silent windows in the direct loop.
	minEnergy := 1e-9 * prefix[n] / float64(n) * float64(m)

	scores := make([]float64, n-m+1)
	for offset := range scores {
		sigEnergy := prefix[offset+m] - prefix[offset]
		if sigEnergy <= minEnergy {
			continue
		}
		denom := patEnergy * math.Sqrt(sigEnergy)
		if denom <= 1e-12 {
			continue
		}
		score := real(sigSpec[offset]) / float64(size) / denom
		scores[offset] = math.Max(-1, math.Min(1, score))
	}
	return scores
}

// fft performs an in-place iterative radix-2 FFT. len(values) must be a power of two.
// The inverse transform is unscaled.
func fft(values []complex128, inverse bool) {
	n := len(values)
	if n <= 1 {
		return
	}
	shift := 64 - bits.Len(uint(n-1))
	for i := range values {
		j := int(bits.Reverse64(uint64(i)) >> shift)
		if i < j {
			values[i], values[j] = values[j], values[i]
		}
	}
	sign := -1.0
	if inverse {
		sign = 1.0
	}
	for length := 2; length <= n; length <<= 1 {
		angle := sign * 2 * math.Pi / float64(length)
		step := complex(math.Cos(angle), math.Sin(angle))
		half := length / 2
		for start := 0; start < n; start += length {
			w := complex(1, 0)
			for k := 0; k < half; k++ {
				even := values[start+k]
				odd := values[start+k+half] * w
				values[start+k] = even + odd
				values[start+k+half] = even - odd
				w *= step
			}
		}
	}
}
