// Command pix is a minimal image viewer.
package main

import (
	"flag"
	"fmt"
	"os"

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
	all := flag.Bool("all", false, "include subdirectories, recursively")
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

	// The marked files are printed to stdout on exit. Keep the real stdout
	// for that alone and point fd 1 at stderr, so nothing else (Fyne, GL
	// drivers, C decoders, children) can write into a pipe.
	stdout := reserveStdout()

	entries, start, roots, err := scan.Resolve(flag.Args(), scan.Options{Sort: order, Recursive: *all, Taken: imgload.Taken})
	if err == nil && len(entries) == 0 {
		err = fmt.Errorf("no images found")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pix: %v\n", err)
		os.Exit(1)
	}

	dir := "" // ad-hoc list: not watched
	if len(roots) == 1 {
		dir = roots[0]
	}
	err = ui.Run(ui.Options{
		Entries:    entries,
		Start:      start,
		Dir:        dir,
		Recursive:  *all,
		Sort:       order,
		Fullscreen: *fullscreen,
		Sidebar:    *sidebar,
		Loader:     imgload.NewLoader(10, 4),
		OutDir:     *outDir,
		Stdout:     stdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pix: %v\n", err)
		os.Exit(1)
	}
}
