package scan

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The birth time is used only when statx says it filled it in (sshfs, for
// one, leaves STATX_BTIME out of the mask), and not when it is zero.
func TestCreatedOf(t *testing.T) {
	mt := unix.StatxTimestamp{Sec: 1714564800, Nsec: 5}
	bt := unix.StatxTimestamp{Sec: 1731000000, Nsec: 7}
	for _, c := range []struct {
		name string
		st   unix.Statx_t
		want unix.StatxTimestamp
	}{
		{"btime", unix.Statx_t{Mask: unix.STATX_BASIC_STATS | unix.STATX_BTIME, Btime: bt, Mtime: mt}, bt},
		{"no btime in mask", unix.Statx_t{Mask: unix.STATX_BASIC_STATS, Btime: bt, Mtime: mt}, mt},
		{"zero btime", unix.Statx_t{Mask: unix.STATX_BASIC_STATS | unix.STATX_BTIME, Mtime: mt}, mt},
	} {
		if got := createdOf(&c.st); !got.Equal(time.Unix(c.want.Sec, int64(c.want.Nsec))) {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}
