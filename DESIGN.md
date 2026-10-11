# pix — a minimal image viewer

Single static-ish Go binary, Fyne GUI (OpenGL via GLFW), targets Linux. Primary
target desktop: Omarchy (Arch + Hyprland, Wayland). Must also work on X11.
No config file. Behaviour comes only from flags and keys.

Module name: `pix`. Binary: `pix`. Go 1.26 (go.mod already set up, deps fetched).

## Layout

    main.go                 flags, wiring, exit codes
    internal/scan/          directory listing, image sniffing, natural sort, dir watch
    internal/imgload/       decoding (all formats), EXIF orientation, async LRU cache
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
    ModTime time.Time
    Size    int64
}

type Sort int
const (
    SortName  Sort = iota // natural sort on Name, case-insensitive ("img2" < "img10")
    SortMTime             // oldest first; ties broken by natural name
)

// Resolve turns CLI args into a playlist.
//   no args            -> cwd listing, start=0
//   one dir            -> that dir's listing, start=0
//   one file           -> its parent dir's listing, start=index of that file
//                         (the file is included even if IsImage says no)
//   several args       -> ad-hoc list of exactly those paths that exist
//                         (dirs expanded one level, non-images dropped), dir=""
// Returns an error only when nothing is viewable.
func Resolve(args []string, sort Sort) (entries []Entry, start int, dir string, err error)

// List returns the readable images directly inside dir (no recursion),
// hidden files (leading dot) excluded, sorted. Symlinks followed.
func List(dir string, sort Sort) ([]Entry, error)

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

// Prefetch schedules decodes without blocking; already-cached paths are noop.
func (l *Loader) Prefetch(paths ...string)

// Invalidate drops path from the cache (used after dir rescans when mtime changed).
func (l *Loader) Invalidate(path string)

// SetByteBudget caps cached pixel bytes (sum of len(img.Pix); default
// DefaultByteBudget = 1.5 GiB) on top of the count cap. LRU entries beyond
// either cap are evicted; the image last asked for by Get is always kept.
func (l *Loader) SetByteBudget(n int64)
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
    Dir        string         // "" for ad-hoc lists (no watching)
    Sort       scan.Sort
    Fullscreen bool           // start fullscreen
    Sidebar    bool           // sidebar visible at start
    Loader     *imgload.Loader
}

// Run builds the Fyne app and blocks until the window closes.
func Run(opts Options) error
```

## UI behaviour

Window: HBox( sidebar | VBox( viewport, status ) ). Sidebar fixed width ~220pt,
hidden in fullscreen and when toggled off. Window title = file name.
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
free zoom resets to fit). Rotation resets to 0 on image change.

### Keys (global, via Canvas().SetOnTypedKey / SetOnTypedRune / AddShortcut)

    PageDown, Space, Right, n     next image
    PageUp, Backspace, Left, p    previous image
    Home / End                    first / last
    f                             toggle fullscreen; entering fullscreen -> Fit
    z                             1:1
    x                             Fit
    + / = / -                     zoom in / out (×1.25 steps), around viewport centre
    [ / ]                         rotate 90° CCW / CW (view only), stays in current mode (re-fit if Fit)
    Ctrl+C                        copy image to clipboard (PNG, *as displayed rotation applied*)
    Ctrl+Shift+C                  copy absolute path as text
    Tab, b                        toggle sidebar (both; Fyne may eat Tab for focus)
    r                             rescan directory now
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

widget.List of file names. Current item selected and scrolled into view
whenever the image changes. Clicking an item shows it. Focus must return to
the viewport so keys keep working (unfocus the list after selection).

### Status bar

One line, small text, left aligned:
`name.jpg   12/87   4000×3000   33%   ↻90°` (rotation part only when ≠0).
Show `error: <msg>` in place of dims if decode failed (the viewport shows a
plain message too; paging must still work past broken files).

### Directory watching

If Dir != "", scan.Watch(Dir) -> re-List, keep the current file selected by
path (if it vanished, clamp index), refresh sidebar. Also triggered by `r`.

### Prefetch

After showing index i, Prefetch(i+1, i-1, i+2). Loader cap 6.

## main.go flags

    pix [flags] [path ...]
      -sort name|mtime   (default name)
      -fullscreen        start fullscreen
      -sidebar=false     start with sidebar hidden
      -version

Exit 1 with a one-line stderr message when nothing viewable is found.
Decode failures are not fatal.

## Build

    go build -tags wayland -o pix .     # Omarchy / Hyprland (native Wayland)
    go build -o pix .                   # X11 (also works under XWayland)

Makefile with `make`, `make x11`, `make test`, `make install` (~/.local/bin).
Keep `go vet ./...` clean. gofmt everything.
