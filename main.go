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

const version = "0.1.0"

func main() {
	sortFlag := flag.String("sort", "name", "sort order: name|mtime")
	fullscreen := flag.Bool("fullscreen", false, "start fullscreen")
	sidebar := flag.Bool("sidebar", true, "start with the sidebar visible")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: pix [flags] [path ...]\n")
		flag.PrintDefaults()
	}
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
	default:
		fmt.Fprintf(os.Stderr, "pix: -sort must be name or mtime, not %q\n", *sortFlag)
		os.Exit(2)
	}

	entries, start, dir, err := scan.Resolve(flag.Args(), order)
	if err == nil && len(entries) == 0 {
		err = fmt.Errorf("no images found")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pix: %v\n", err)
		os.Exit(1)
	}

	err = ui.Run(ui.Options{
		Entries:    entries,
		Start:      start,
		Dir:        dir,
		Sort:       order,
		Fullscreen: *fullscreen,
		Sidebar:    *sidebar,
		Loader:     imgload.NewLoader(6, 2),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pix: %v\n", err)
		os.Exit(1)
	}
}
