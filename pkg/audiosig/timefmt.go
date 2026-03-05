package audiosig

import (
	"fmt"
	"time"
)

// FormatHMS formats a duration as HH:MM:SS.
// Inputs: duration.
// Outputs: formatted string.
// Example usage:
//
//	s := FormatHMS(3661*time.Second) // "01:01:01"
//
// Notes: Truncates sub-second precision.
func FormatHMS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int64(d.Seconds())
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// FormatHMSMillis formats a duration as HH:MM:SS.mmm.
// Inputs: duration.
// Outputs: formatted string.
// Example usage:
//
//	s := FormatHMSMillis(1500*time.Millisecond) // "00:00:01.500"
//
// Notes: Rounds to nearest millisecond.
func FormatHMSMillis(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	msTotal := int64(d.Round(time.Millisecond) / time.Millisecond)
	h := msTotal / (3600 * 1000)
	m := (msTotal % (3600 * 1000)) / (60 * 1000)
	s := (msTotal % (60 * 1000)) / 1000
	ms := msTotal % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}
