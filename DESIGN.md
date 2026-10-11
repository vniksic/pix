# pix — a minimal image viewer

Single static-ish Go binary, Fyne GUI (OpenGL via GLFW), targets Linux. Primary
target desktop: Omarchy (Arch + Hyprland, Wayland). Must also work on X11.
No config file. Behaviour comes only from flags and keys.

Module name: `pix`. Binary: `pix`. Go 1.26 (go.mod already set up, deps fetched).

## Layout

    main.go                 flags, wiring, exit codes
    internal/scan/          directory listing (flat or recursive), image sniffing, natural sort, labels, dir watch
    internal/imgload/       decoding (all formats), EXIF orientation and date taken, async LRU cache
    internal/fileops/       copying marked files out, freedesktop trash, opening in another program
    internal/clipboard/     image/text copy via wl-copy (Wayland) or xclip (X11)
    internal/hypr/          Hyprland control-socket client (fullscreen on the current monitor)
    internal/ui/            Fyne app: window, viewport widget, sidebar, status bar, keys

Each internal package must have unit tests (`go test ./...` passes, no display
needed). UI math (fit scale, zoom-around-cursor, pan clamping, rotation
transform) lives in pure functions in internal/ui so it is testable without GL.

## Package contracts (exact signatures; other packages depend on these)

### internal/scan

```go
package scan

type Entry struct {
    Path    string      // absolute path
    Name    string      // base name
    Label   string      // display path (see Resolve)
    ModTime time.Time
    Size    int64
    Taken   time.Time   // date taken; filled when Sort == SortDate (zero otherwise or unknown)
}

type Sort int
const (
    SortName  Sort = iota // natural sort on Name, case-insensitive ("img2" < "img10")
    SortMTime             // oldest first; ties broken by natural name
    SortDate              // by date taken (Options.Taken), files without one fall back
                          // to ModTime; ties by path. Flat across the tree like SortMTime.
)

type Options struct {
    Sort      Sort
    Recursive bool // expand directories with ListTree instead of List (-all)
    Taken     func(path string) (time.Time, bool) // for SortDate; nil -> SortDate behaves like SortMTime
}

// Resolve turns CLI args into a playlist.
//   no args            -> cwd listing, start=0, roots=[cwd]
//   one dir            -> that dir's listing, start=0, roots=[dir]
//   one file           -> its parent dir's listing, start=index of that file,
//                         roots=[parent] (the file is included even if
//                         IsImage says no)
//   several args       -> ad-hoc list of exactly those paths that exist
//                         (dirs expanded, non-images dropped), roots=nil
// Directories are listed with List, or ListTree when opts.Recursive.
// Labels: in the single-root cases, the path relative to the root
// ("sub/dir/photo.jpg", or "photo.jpg" at the top). In ad-hoc lists, the
// path relative to the cwd when the file is under it, else the absolute
// path with $HOME shortened to "~". Ad-hoc lists keep argument order (each
// expanded dir sorted internally) and drop duplicates.
// args == []string{"-"} reads newline-separated paths from os.Stdin (CRLF
// tolerated, blank lines skipped, relative paths against the cwd,
// directories expanded like other ad-hoc args, missing paths skipped),
// labelled as an ad-hoc list.
// Resolve remembers opts.Taken so later List/ListTree rescans keep the
// SortDate order.
// Returns an error only when nothing is viewable.
func Resolve(args []string, opts Options) (entries []Entry, start int, roots []string, err error)

// ResolveReader is what "-" uses (paths read from r); for tests.
func ResolveReader(r io.Reader, opts Options) (entries []Entry, start int, roots []string, err error)

// List returns the readable images directly inside dir (no recursion),
// hidden files (leading dot) excluded, sorted. Symlinks followed.
// Label = Name.
func List(dir string, sort Sort) ([]Entry, error)

// ListTree returns the readable images in root and all its subdirectories,
// Label relative to root. Hidden files and dirs skipped; symlinked files
// followed, symlinked dirs not (no cycles); unreadable subdirs skipped.
// SortName: each directory's own images first (natural order), then its
// subdirectories in natural order, each recursively, so a directory's
// pictures stay together. SortMTime: one flat oldest-first order over the
// whole tree, ties by path.
func ListTree(root string, sort Sort) ([]Entry, error)

// Compare is the order List and ListTree sort by (mtime for SortMTime,
// then tree order on Label, then Path). RelLabel is the Label of path in a
// listing rooted at root (path itself when not under root).
func Compare(a, b Entry, sort Sort) int
func RelLabel(root, path string) string

// IsImage sniffs the first 64 bytes. Must recognise:
// JPEG, PNG, GIF, WebP (RIFF....WEBP), BMP, TIFF (II*\0 / MM\0*),
// HEIC/HEIF (ftyp heic|heix|hevc|mif1|msf1), AVIF (ftyp avif|avis),
// JPEG XL (FF 0A, or 00 00 00 0C 4A 58 4C 20 0D 0A 87 0A).
// Never uses the file extension.
func IsImage(path string) bool

func NaturalLess(a, b string) bool

// Watch calls onChange (debounced ~300ms, from a goroutine) whenever files
// appear/disappear/change in dir. stop() releases the watcher.
func Watch(dir string, onChange func()) (stop func(), err error)

// WatchTree is Watch over root and every non-hidden, non-symlinked
// subdirectory (the tree ListTree lists). Directories created later are
// added as they appear. At most 2000 directories are watched; past that,
// deeper changes need `r`.
func WatchTree(root string, onChange func()) (stop func(), err error)
```

### internal/imgload

```go
package imgload

// Decode opens and decodes path with every supported format registered
// (image/jpeg, image/png, image/gif, x/image/webp, x/image/tiff,
// x/image/bmp, gen2brain/heic, gen2brain/avif, gen2brain/jpegxl), then
// applies EXIF orientation (rwcarlsen/goexif) for JPEG/TIFF/WebP where present.
// Result is always *image.NRGBA (convert if needed) so the renderer has
// one fast path. Animated GIF: first frame only.
func Decode(path string) (*image.NRGBA, error)

// Loader is an async LRU cache in front of Decode.
type Loader struct{ /* unexported */ }

// NewLoader keeps at most capacity decoded images (recommend 6) and runs
// decodes on up to workers goroutines (recommend 2).
func NewLoader(capacity, workers int) *Loader

// Get blocks until path is decoded. Errors are cached too (so a broken
// file does not re-decode on every visit) but evicted like images.
func (l *Loader) Get(path string) (*image.NRGBA, error)

// GetMeta is Get that also returns the file's Meta, cached with the image.
func (l *Loader) GetMeta(path string) (*image.NRGBA, Meta, error)

// Meta is what pix reads from a file besides its pixels.
type Meta struct {
    Taken time.Time // EXIF DateTimeOriginal (fallback: DateTime); zero when absent
}

// ReadMeta parses only the EXIF block (JPEG APP1, TIFF, WebP EXIF chunk,
// HEIC/AVIF where trivially available); never decodes pixels. Errors give
// a zero Meta.
func ReadMeta(path string) Meta

// Taken is ReadMeta(path).Taken and whether it is set (scan.Options.Taken).
func Taken(path string) (time.Time, bool)

// Prefetch schedules decodes without blocking; already-cached paths are noop.
func (l *Loader) Prefetch(paths ...string)

// Invalidate drops path from the cache (used after dir rescans when mtime changed).
func (l *Loader) Invalidate(path string)

// SetByteBudget caps cached pixel bytes (sum of len(img.Pix); default
// DefaultByteBudget = 1.5 GiB) on top of the count cap. LRU entries beyond
// either cap are evicted; the image last asked for by Get is always kept.
func (l *Loader) SetByteBudget(n int64)
```

### internal/fileops

```go
package fileops

type CopyResult struct {
    Src, Dst string
    Skipped  bool  // an identical file was already there (Dst names it)
    Err      error
}

// CopyAll copies each src into dir (created 0755 if missing), never
// overwriting: the destination is the base name; if that exists and is
// byte-identical (size, then content) the copy is Skipped, else name-2.ext,
// name-3.ext ... is tried until free. Two srcs with the same base name in
// one call get distinct names. Copies keep the mtime; a partial copy is
// removed on error. Results are in the order of srcs.
func CopyAll(srcs []string, dir string) []CopyResult

// Trash moves path to the freedesktop trash: the home trash
// ($XDG_DATA_HOME/Trash or ~/.local/share/Trash, files/ + info/*.trashinfo)
// or, on another filesystem, $topdir/.Trash/$uid or $topdir/.Trash-$uid.
// The file is never removed unless the move succeeded. Pure Go, no gio.
func Trash(path string) error

// OpenExternal starts $PIX_OPEN (a program name, argument = path) or
// xdg-open with the path, detached (Setsid), stdio on /dev/null.
func OpenExternal(path string) error
```

### internal/clipboard

```go
package clipboard

// CopyImage PNG-encodes img and hands it to the system clipboard.
// Backend selection: if $WAYLAND_DISPLAY is set and wl-copy is in PATH ->
//   `wl-copy --type image/png` (stdin). Else if xclip in PATH ->
//   `xclip -selection clipboard -t image/png -i`. Else ErrNoBackend.
// Both tools fork and keep serving the selection after we return, which is
// what X11/Wayland require. Do not wait on the child beyond stdin close +
// a short timeout; wl-copy may legitimately stay alive.
func CopyImage(img image.Image) error

// CopyText puts s on the clipboard with the same backend selection
// (wl-copy / xclip -selection clipboard).
func CopyText(s string) error

var ErrNoBackend = errors.New("clipboard: neither wl-copy nor xclip found")
```

### internal/ui

```go
package ui

type Options struct {
    Entries    []scan.Entry
    Start      int
    Dir        string         // listing root; "" for ad-hoc lists (no watching)
    Recursive  bool           // Dir listed with ListTree, watched with WatchTree
    Sort       scan.Sort
    Fullscreen bool           // start fullscreen
    Sidebar    bool           // sidebar visible at start
    Loader     *imgload.Loader
    OutDir     string         // -out: copy destination for marked files ("" = none)
    Stdout     io.Writer      // receives the marked paths at exit (default os.Stdout)
}

// Run builds the Fyne app and blocks until the window closes, then copies
// marked files not yet copied to OutDir and prints the marked paths to
// Stdout (see "Marks"). A failed exit copy is returned as the error.
func Run(opts Options) error
```

## UI behaviour

Window: HBox( sidebar | VBox( viewport, status ) ). Sidebar fixed width ~280pt,
hidden in fullscreen and when toggled off. Window title = the entry's Label.
Background: near-black (#111) behind the image.

### Viewport (custom Fyne widget wrapping canvas.Raster)

The raster generator receives the viewport size in *device pixels* and returns
an *image.NRGBA of that size. Draw with golang.org/x/image/draw.Transform
using one affine matrix that composes: rotation about the image centre,
scale, translation (pan). Only the visible region is computed, so huge images
cost no more than the screen does. Rotation is view-only; never re-encode.

Two interpolators: draw.ApproxBiLinear while the user is dragging/zooming
(and whenever the scale is exactly 1.0, where it is a plain copy), and
draw.CatmullRom for a "settle" re-render 120ms after interaction stops.
Cache the last settled frame; do not re-render if nothing changed.

Below 0.5 both interpolators read from a mip level instead of the original:
the image box-downsampled by 2, repeatedly (smallest level whose scale is
still >= the view scale, so the remaining downscale is 1×–2×). Levels are
built lazily off the main goroutine (parallel over rows), cached with the
image and dropped when it changes; until a level exists the best existing
one is used. 1:1 and zoom-in always read the original.

Modes:
- Fit: scale = min(vw/rw, vh/rh) where rw,rh are the *rotated* image dims;
  never upscale above 1.0 unless the user zooms; image centred.
- 1:1: scale 1.0, centred on first entry (or on the cursor point if coming
  from a wheel zoom).
- Free zoom: any scale from wheel / +/-; zoom around cursor position
  (the image point under the cursor stays under the cursor).

Pan: left-drag moves the image by the drag delta (device px). Shift held at
MouseDown => delta × 4. Pan is clamped so the image never leaves the
viewport entirely: if the image is smaller than the viewport on an axis it
is centred on that axis; otherwise edges stay within the viewport.
Cursor turns to a grab hand while dragging if Fyne supports it (desktop.Cursorable).

Changing image: keep the current mode (fit stays fit; 1:1 stays 1:1 recentred;
free zoom resets to fit). Rotation resets to 0 on image change, unless
the photo has a remembered rotation (below).

View lock (`l`, View.Locked): while locked, changing image keeps the mode,
scale, rotation and pan offsets instead (offsets clamped for the new image
size; Fit re-fits with the kept rotation), so a series of shots can be
compared at the same spot. "loading"/error messages keep the rotation too.

Remembered rotation: `[` / `]` store the resulting rotation (0..3, 0
included) for the current photo in a map on the viewer, keyed by absolute
path, in memory only (never written to disk). When an image is installed
(the load loop's fyne.Do, after View.SetImage's lock logic) its rotation is
the pure rotationFor: the remembered value for its path, else the rotation
the lock carried over (when locked), else 0; it is applied with
View.SetRot, which goes through Rotate (Fit re-fits). The lock still keeps
mode, scale and pan. The map survives rescans and `--all` lists; a trashed
file's entry is dropped, a renamed file simply starts afresh. Ctrl+C copies
the displayed rotation, so it includes the remembered one.

### Keys (global, via Canvas().SetOnTypedKey / SetOnTypedRune / AddShortcut)

    PageDown, Space, Right, n     next image
    PageUp, Backspace, Left, p    previous image
    Home / End                    first / last
    f                             toggle fullscreen; entering fullscreen -> Fit
    z                             1:1; in fullscreen also hides the status bar (shown again by Fit via x/double-click, or leaving fullscreen)
    x                             Fit
    + / = / -                     zoom in / out (×1.25 steps), around viewport centre
    [ / ]                         rotate 90° CCW / CW (view only), stays in current mode (re-fit if Fit); remembered per photo for the session
    Ctrl+C                        copy image to clipboard (PNG, *as displayed rotation applied*)
    Ctrl+Shift+C                  copy absolute path as text
    Tab, b                        toggle sidebar (both; Fyne may eat Tab for focus)
    r                             rescan directory now
    m                             mark / unmark the current image
    Enter                         copy marked files not yet copied to -out (in a goroutine)
    Delete                        first press asks; a second press within 1.5s moves the file to the trash
    l                             lock / unlock the view across image changes
    o                             open the file in another program ($PIX_OPEN or xdg-open)
    Escape                        leave fullscreen if in it, else quit
    q                             quit

Mouse: wheel = zoom around cursor (×1.1 per notch; GLFW reports 1.5 per
notch on Wayland vs 1 on X11, so deltas are divided by 1.5 on Wayland); left-drag = pan;
Shift+left-drag = fast pan; double-click = toggle Fit/1:1.

### Fullscreen

pix's own `fullscreen` flag drives everything (sidebar hiding, Fit,
Escape); Fyne's flag is never read. Putting the *window* in fullscreen:

- Fyne's `SetFullScreen` uses GLFW's monitor for the window, which on
  Wayland does not exist (no global positions), so it always picks the
  primary monitor.
- On Wayland under Hyprland (`HYPRLAND_INSTANCE_SIGNATURE` set and its
  control socket present) pix instead asks Hyprland over
  `$XDG_RUNTIME_DIR/hypr/$SIG/.socket.sock` (`/tmp/hypr/...` on old
  versions), one request per connection: `j/clients` to find our mapped
  window by pid (and skip the request if it is already in the wanted
  state), then
  - Lua config (Hyprland 0.56+, where `dispatch X` means
    `return hl.dispatch(X)`):
    `dispatch hl.dsp.window.fullscreen_state({ internal = 2, client = 2, action = "set", window = "pid:<pid>" })`
    (`0`/`0` to leave); it targets the window directly, no focus change;
  - hyprlang config (0.42+):
    `[[BATCH]]dispatch focuswindow pid:<pid>;dispatch fullscreenstate 2 2`
    (`0 0` to leave).

  The Lua form is tried first; on a reply that is not `ok` the hyprlang
  batch follows (each syntax is rejected harmlessly by the other config
  type), and the form that worked is used alone from then on. The
  compositor keeps the window on its monitor and GLFW sees an ordinary
  configure/resize. No cgo, no hyprctl.
- The request runs off the main goroutine (1s timeout). Hyprland can only
  act on a mapped window, so `-fullscreen` sends it on the viewport's first
  frame, retrying 5 × 100ms while the window is unknown. Any other failure
  is logged once and Fyne's `SetFullScreen` is used instead (and to leave a
  fullscreen Fyne entered).
- X11 and other Wayland compositors use Fyne directly.

### Sidebar

widget.List of entries. Current item selected and scrolled into view
whenever the image changes. Clicking an item shows it. Focus must return to
the viewport so keys keep working (unfocus the list after selection).

Each item shows the Label split at its last `/`: the file name, and under
it the directory part in smaller, dimmer text (caption size, low
importance). widget.List needs one item height, so the template always has
both lines; an item without a directory part hides the second line and
centres the name. Two-line items are used in recursive single-root mode
(new subdirectories may appear) and for ad-hoc lists where some Label has
a directory part; otherwise (plain directory listing) items are one line.
Long text is truncated with an ellipsis. A marked entry's name starts with
"★ " (same row height either way).

### Status bar

One line, small text, left aligned, parts three spaces apart:

    sub/name.jpg ★   12/87   4000×3000   2024-05-01 14:03   33%   ↻90°   lock   3 marked   <message>

The Label, "★" when the image is marked, position, dimensions, date taken
(EXIF, "2006-01-02 15:04", only when present), zoom, rotation (only when
≠0), "lock" while the view is locked, "N marked" when N > 0, and a transient
message (2s). Show `error: <msg>` in place of dims if decode failed (the
viewport shows a plain message too; paging must still work past broken
files). It is composed by the pure function statusText (tested headless).

### Marks, copying out, trash

- `m` toggles a mark on the current file (a set keyed by path; marks of
  files that leave the listing are kept and count again if they return).
- `Enter` copies the marked files not copied yet to `-out` with
  fileops.CopyAll, in playlist order, off the main goroutine (one copy at a
  time), then flashes `copied 3 to DIR`, `copied 2, skipped 1 (already
  there)`, or the first error. Without `-out`: `no -out directory`.
- Quitting (q, Escape, closing the window): the window's OnClosed handler
  (main goroutine, every quit path) snapshots the playlist order and sets
  `quitting`, after which callbacks leave the state alone. Once
  ShowAndRun returns, Run waits for clipboard, copy-out and trash
  goroutines, then, if `-out` is set, synchronously copies the marked files
  not yet copied (one summary line on stderr; a failure exits 1), and
  finally prints every marked path, absolute, one per line, in playlist
  order, to stdout.
- Nothing else writes to stdout: main moves the real stdout to a
  close-on-exec descriptor for that list and points fd 1 at stderr, so
  Fyne, GL drivers, C decoders and child processes cannot pollute a pipe.
- `Delete` (KeyDown, so auto-repeat never counts as a second press) flashes
  `Delete again to trash <name>`; a second press on the same file within
  1.5s runs fileops.Trash in a goroutine. On success the entry leaves the
  playlist and the marks, its decode is invalidated, the next image is
  shown (the previous if it was the last; "no images" if none) and
  `trashed <name>` flashes. On error the error is flashed and the entry
  stays. Any other key (modifiers alone excepted) cancels the pending
  Delete.
- `o` runs fileops.OpenExternal on the current file in a goroutine and
  flashes an error, if any.

### Directory watching

If Dir != "", scan.Watch(Dir) (scan.WatchTree when Recursive) -> re-List
(ListTree when Recursive), keep the current file selected by path (if it
vanished, clamp index), refresh sidebar. Also triggered by `r`. The file
named on the command line stays in the list while it exists even if the
listing leaves it out (hidden, unsniffable); it is inserted at its sorted
position with its Label recomputed relative to Dir. Ad-hoc lists are not
watched; `r` re-stats them.

### Prefetch

Paging is gated on display, so a held PageDown (or any next/previous key)
shows every image instead of skipping to wherever the key repeat got to. A
step that arrives while the current image is still loading, or less than
`minDwell` (100ms, a constant in internal/ui/paging.go) after it was
rendered, is not taken but kept as the one pending step (not a queue; the
newest direction wins). When the image renders (or its error message does)
the pending step runs after whatever is left of the dwell. A single press on
an image already shown is immediate. Home/End, sidebar clicks, quitting,
trashing the current file and rescans that remove it drop the pending step.
The load loop still coalesces requests as a safety net. The decision is the
pure gateStep.

Look-ahead follows the last step direction d (+1 until the first step):
once image i is decoded, one Prefetch(i+d, i+2d, i+3d, i+4d, i-d) call
(prefetchOrder) queues them in that order, ahead of older prefetches, so
i+d is decoded first. main uses NewLoader(10, 4): four decodes in parallel,
and a cache holding the look-ahead plus a few images behind.

## main.go flags

    pix [flags] [path ...]
    pix [flags] -        paths read from stdin, one per line (ad-hoc list)
      -all               include subdirectories recursively (also --all); applies
                         to the cwd, to directory args, and to a single file arg
                         (its parent is listed recursively, starting at the file)
      -sort name|mtime|date  (default name; date = EXIF date taken, files
                         without one by mtime; mtime and date are flat over the tree)
      -out DIR           copy destination for marked files (Enter, and on quit)
      -fullscreen        start fullscreen
      -sidebar=false     start with sidebar hidden
      -version

Exit 1 with a one-line stderr message when nothing viewable is found, or
when the exit copy to -out fails. Decode failures are not fatal. Stdout
carries only the marked paths printed at exit.

## Build

    go build -tags wayland -o pix .     # Omarchy / Hyprland (native Wayland)
    go build -o pix .                   # X11 (also works under XWayland)

Makefile with `make`, `make x11`, `make test`, `make install` (~/.local/bin).
Keep `go vet ./...` clean. gofmt everything.
