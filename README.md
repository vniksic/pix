# pix

A deliberately small image viewer for Linux, written in Go with Fyne. One
binary, no config file, meant to be launched from a terminal.

    pix                    # view the images in the current directory
    pix photo.jpg          # start at photo.jpg, page through its siblings
    pix ~/Pictures         # view a directory
    pix a.png b.jpg c.webp # view exactly these files

Files are detected by content, not extension, so PDFs, videos and misnamed
files never show up in the list. Formats: JPEG (with EXIF orientation), PNG,
GIF, WebP, TIFF, BMP, HEIC, AVIF, JPEG XL.

## Keys

| Key                              | Action                                   |
|----------------------------------|------------------------------------------|
| PageDown, Space, Right, n        | next image                               |
| PageUp, Backspace, Left, p       | previous image                           |
| Home / End                       | first / last image                       |
| f                                | toggle fullscreen (enters in fit mode)   |
| z                                | 1:1 pixels (in fullscreen also hides the status bar until x) |
| x                                | fit to window                            |
| + / = / -                        | zoom in / out                            |
| [ / ]                            | rotate 90° left / right (view only)      |
| Ctrl+C                           | copy image to clipboard (as displayed)   |
| Ctrl+Shift+C                     | copy file path to clipboard              |
| Tab, b                           | toggle the file list                     |
| r                                | rescan the directory                     |
| Escape                           | leave fullscreen, or quit                |
| q                                | quit                                     |

Mouse: wheel zooms around the cursor, left-drag pans, Shift+left-drag pans
four times faster, double-click toggles fit and 1:1.

The directory is watched, so files that appear or vanish show up without a
restart.

## Flags

    -sort name|mtime   sort order (default name, natural order: img2 < img10)
    -fullscreen        start fullscreen
    -sidebar=false     start with the file list hidden
    -version

## Build

Needs Go 1.26 (the toolchain downloads itself if older), a C compiler, and
the usual GL/X11/Wayland headers (on Debian: `libgl-dev libegl-dev
libx11-dev libxcursor-dev libxrandr-dev libxinerama-dev libxi-dev
libxxf86vm-dev libxkbcommon-dev libwayland-dev`).

    make            # builds ./pix (Wayland and X11 in one binary)
    make test
    make install    # copies to ~/.local/bin

The binary links only libc, libGL, libwayland-client, libX11 and libffi,
all present on any Linux desktop, so it can be copied between machines.

Clipboard copy uses `wl-copy` on Wayland and `xclip` on X11; install
`wl-clipboard` or `xclip` if Ctrl+C reports no backend.

Fyne picks Wayland when `WAYLAND_DISPLAY` is set and X11 otherwise. Set
`FYNE_PLATFORM=x11` or `FYNE_PLATFORM=wayland` to force one.

Fullscreen on Hyprland (Wayland, Lua or hyprlang config) goes through
Hyprland's IPC socket, so it lands on the monitor the window is on;
elsewhere Fyne does it, which on other Wayland compositors always picks the
primary monitor.
