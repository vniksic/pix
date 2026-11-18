// Command pix is a minimal image viewer.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"pix/internal/fileops"
	"pix/internal/imgload"
	"pix/internal/scan"
	"pix/internal/ui"
)

var version = "dev" // set at build time: -ldflags "-X main.version=v0.1.0"

// usage is flag.PrintDefaults with the flags spelled --name; the flag
// package accepts both spellings.
func usage() {
	w := flag.CommandLine.Output()
	fmt.Fprintf(w, "usage: pix [flags] [path ...]\n       pix [flags] -     (read paths from stdin)\n")
	flag.VisitAll(func(f *flag.Flag) {
		name, usage := flag.UnquoteUsage(f)
		s := "  --" + f.Name
		if name != "" {
			s += " " + name
		}
		if f.DefValue != "" && f.DefValue != "false" {
			usage += " (default " + f.DefValue + ")"
		}
		fmt.Fprintf(w, "%-20s %s\n", s, usage)
	})
}

func main() {
	sortFlag := flag.String("sort", "name", "sort `ORDER`: name|mtime|date (date taken, else mtime)")
	outDir := flag.String("out", "", "copy marked files (m) into `DIR` on Enter and on quit")
	shuffle := flag.Bool("shuffle", false, "random order, different on every run (instead of --sort)")
	all := flag.Bool("all", false, "include subdirectories, recursively")
	since := flag.String("since", "", "only pictures created in the last `AGE`: a number and m, h, d or w\n                     (minutes, hours, days, weeks: 90m, 12h, 1d, 2w); implies --all")
	fullscreen := flag.Bool("fullscreen", false, "start fullscreen")
	sidebar := flag.Bool("sidebar", true, "start with the sidebar visible")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("pix", version)
		return
	}

	var order scan.Sort
	switch *sortFlag {
	case "name":
		order = scan.SortName
	case "mtime":
		order = scan.SortMTime
	case "date":
		order = scan.SortDate
	default:
		fmt.Fprintf(os.Stderr, "pix: -sort must be name, mtime or date, not %q\n", *sortFlag)
		os.Exit(2)
	}
	if *shuffle {
		explicit := false
		flag.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "sort" })
		if explicit {
			fmt.Fprintln(os.Stderr, "pix: --shuffle and --sort do not go together")
			os.Exit(2)
		}
		order = scan.SortShuffle
	}

	// The cutoff is fixed now, so pictures do not drop out of the list as
	// the session goes on, rescans included.
	var cutoff time.Time
	sinceSet := false
	flag.Visit(func(f *flag.Flag) { sinceSet = sinceSet || f.Name == "since" })
	if sinceSet {
		age, err := parseAge(*since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pix: %v\n", err)
			os.Exit(2)
		}
		cutoff = time.Now().Add(-age)
	}

	// The copy target is checked now so a wrong one does not come to light
	// at Enter, or at quit after the culling is done. It need not exist
	// yet (the first copy creates it); existing files in it are fine, a
	// copy never overwrites them.
	if *outDir != "" {
		if err := fileops.CheckWritable(*outDir); err != nil {
			fmt.Fprintf(os.Stderr, "pix: --out: %v\n", err)
			os.Exit(2)
		}
	}

	// The marked files are printed to stdout on exit. Keep the real stdout
	// for that alone and point fd 1 at stderr, so nothing else (Fyne, GL
	// drivers, C decoders, children) can write into a pipe.
	stdout := reserveStdout()

	// Only the cheap checks happen here (a missing path, an unreadable
	// directory or stdin); the listing itself fills in once the window is
	// open, which may end in "no images".
	target, err := scan.Prepare(flag.Args(), scan.Options{Sort: order, Recursive: *all || sinceSet, Taken: imgload.Taken, Since: cutoff})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pix: %v\n", err)
		os.Exit(1)
	}

	err = ui.Run(ui.Options{
		Target:     target,
		Fullscreen: *fullscreen,
		Sidebar:    *sidebar,
		Loader:     imgload.NewLoader(10, 4),
		Since:      *since,
		OutDir:     *outDir,
		Stdout:     stdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pix: %v\n", err)
		os.Exit(1)
	}
}

// ageUnits are the units of --since's AGE.
var ageUnits = map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}

// parseAge reads --since's AGE: a positive whole number followed by one
// unit, m (minutes), h (hours), d (days) or w (weeks), as in 90m or 2w.
func parseAge(s string) (time.Duration, error) {
	bad := fmt.Errorf("--since must be a whole number above zero followed by m, h, d or w (like 90m, 12h, 1d, 2w), not %q", s)
	if len(s) < 2 {
		return 0, bad
	}
	unit, ok := ageUnits[s[len(s)-1]]
	digits := s[:len(s)-1]
	if !ok || strings.Trim(digits, "0123456789") != "" {
		return 0, bad
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err == nil && n <= 0 {
		return 0, bad
	}
	if err != nil || n > math.MaxInt64/int64(unit) {
		return 0, fmt.Errorf("--since %s goes back too far", s)
	}
	return time.Duration(n) * unit, nil
}
