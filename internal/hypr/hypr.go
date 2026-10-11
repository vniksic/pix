// Package hypr talks to the Hyprland compositor over its control socket
// (the one hyprctl uses), without cgo and without running hyprctl.
//
// pix uses it for fullscreen: on Wayland a window has no global position,
// so GLFW (and Fyne) cannot tell which monitor it is on and fullscreen on
// the primary one. Asking Hyprland to fullscreen the window instead keeps
// it on the monitor it is on; GLFW sees an ordinary configure and resize.
package hypr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// timeout bounds the dial and the whole request/reply exchange.
var timeout = time.Second

// ErrNoWindow means Hyprland has no mapped window for the pid (yet).
var ErrNoWindow = errors.New("hypr: no window for this process")

// Available reports whether we run under Hyprland and its control socket
// exists.
func Available() bool {
	_, ok := socketPath(os.Getenv, exists)
	return ok
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// socketPath finds the control socket: $XDG_RUNTIME_DIR/hypr/$SIG/.socket.sock
// on Hyprland >= 0.40, /tmp/hypr/$SIG/.socket.sock before that.
func socketPath(getenv func(string) string, exists func(string) bool) (string, bool) {
	sig := getenv("HYPRLAND_INSTANCE_SIGNATURE")
	if sig == "" || strings.ContainsRune(sig, '/') {
		return "", false
	}
	var dirs []string
	if rt := getenv("XDG_RUNTIME_DIR"); rt != "" {
		dirs = append(dirs, filepath.Join(rt, "hypr"))
	}
	dirs = append(dirs, "/tmp/hypr")
	for _, d := range dirs {
		if p := filepath.Join(d, sig, ".socket.sock"); exists(p) {
			return p, true
		}
	}
	return "", false
}

// Fullscreen modes of Hyprland's fullscreenstate dispatcher.
const (
	modeNone       = 0
	modeFullscreen = 2
)

// client is the part of a `j/clients` entry we read.
type client struct {
	Pid              int  `json:"pid"`
	Mapped           bool `json:"mapped"`
	Hidden           bool `json:"hidden"`
	Fullscreen       int  `json:"fullscreen"`
	FullscreenClient int  `json:"fullscreenClient"`
}

// SetFullscreen makes the window of process pid fullscreen (on) or not on
// the monitor it is currently on. It returns ErrNoWindow (possibly wrapped)
// when Hyprland does not know a mapped window for pid, e.g. right after
// start; any other failure, including a reply that is not "ok" for every
// command, is an error too.
//
// Requests sent, each on its own connection:
//
//	j/clients
//
// then, for a Lua config (Hyprland 0.56+ with hyprland.lua), where the
// socket's "dispatch X" is evaluated as `return hl.dispatch(X)`:
//
//	dispatch hl.dsp.window.fullscreen_state({ internal = 2, client = 2, action = "set", window = "pid:<pid>" })
//
// (0 and 0 to leave), or for a hyprlang config (hyprland.conf, 0.42+):
//
//	[[BATCH]]dispatch focuswindow pid:<pid>;dispatch fullscreenstate 2 2
//	[[BATCH]]dispatch focuswindow pid:<pid>;dispatch fullscreenstate 0 0
//
// The Lua form is tried first; Hyprland with a hyprlang config rejects it
// as an invalid dispatcher (and a Lua one fails to parse the hyprlang
// batch), so trying one and then the other is harmless either way. The
// form that worked is remembered for the rest of the process.
//
// The clients query comes first because a hyprlang batch runs every
// command even when focuswindow fails (and up to at least Hyprland 0.45
// focuswindow replied "ok" for an unknown window), so fullscreenstate,
// which acts on the active window, would hit some other window. It also
// makes the call idempotent: fullscreenstate toggles back to none when the
// window is already in the requested state, so nothing is sent then.
func SetFullscreen(pid int, on bool) error {
	path, ok := socketPath(os.Getenv, exists)
	if !ok {
		return errors.New("hypr: Hyprland control socket not found")
	}
	return setFullscreen(path, pid, on)
}

// Dispatch syntaxes, remembered in form once one has worked.
const (
	formUnknown int32 = iota
	formLua
	formHyprlang
)

var form atomic.Int32

// replyError is a well-formed refusal from Hyprland (as opposed to a
// socket failure), which is what makes trying the other syntax worthwhile.
type replyError struct{ err error }

func (e *replyError) Error() string { return e.err.Error() }
func (e *replyError) Unwrap() error { return e.err }

func setFullscreen(path string, pid int, on bool) error {
	reply, err := request(path, "j/clients")
	if err != nil {
		return err
	}
	var clients []client
	if err := json.Unmarshal([]byte(reply), &clients); err != nil {
		return fmt.Errorf("hypr: clients: %w (reply %q)", err, abbrev(reply))
	}
	var me *client
	for i := range clients {
		if c := &clients[i]; c.Pid == pid && c.Mapped && !c.Hidden {
			me = c
			break
		}
	}
	if me == nil {
		return ErrNoWindow
	}
	mode := modeNone
	if on {
		mode = modeFullscreen
	}
	if me.Fullscreen == mode && me.FullscreenClient == mode {
		return nil
	}

	switch form.Load() {
	case formLua:
		return dispatchLua(path, pid, mode)
	case formHyprlang:
		return dispatchHyprlang(path, pid, mode)
	}
	errLua := dispatchLua(path, pid, mode)
	var re *replyError
	if !errors.As(errLua, &re) {
		if errLua == nil {
			form.Store(formLua)
		}
		return errLua
	}
	err = dispatchHyprlang(path, pid, mode)
	if err == nil {
		form.Store(formHyprlang)
		return nil
	}
	return fmt.Errorf("%w (Lua syntax: %v)", err, errLua)
}

// luaRequest is the Lua-config request: fullscreen_state with action "set"
// targets the window by selector, so no focus change is needed.
func luaRequest(pid, mode int) string {
	return fmt.Sprintf(`dispatch hl.dsp.window.fullscreen_state({ internal = %d, client = %d, action = "set", window = "pid:%d" })`, mode, mode, pid)
}

// hyprlangCommands is the hyprlang-config batch: fullscreenstate acts on
// the active window, so focus ours first.
func hyprlangCommands(pid, mode int) []string {
	return []string{
		fmt.Sprintf("dispatch focuswindow pid:%d", pid),
		fmt.Sprintf("dispatch fullscreenstate %d %d", mode, mode),
	}
}

func dispatchLua(path string, pid, mode int) error {
	reply, err := request(path, luaRequest(pid, mode))
	if err != nil {
		return err
	}
	return checkOK(reply, 1)
}

func dispatchHyprlang(path string, pid, mode int) error {
	cmds := hyprlangCommands(pid, mode)
	reply, err := request(path, "[[BATCH]]"+strings.Join(cmds, ";"))
	if err != nil {
		return err
	}
	return checkOK(reply, len(cmds))
}

// checkOK accepts a batch reply of exactly n "ok"s. Hyprland separates the
// per-command replies with "\n\n\n".
func checkOK(reply string, n int) error {
	f := strings.Fields(reply)
	if len(f) == n && !slices.ContainsFunc(f, func(s string) bool { return s != "ok" }) {
		return nil
	}
	msg := strings.TrimSpace(reply)
	if strings.Contains(msg, "No such window") {
		return &replyError{fmt.Errorf("%w: %s", ErrNoWindow, msg)}
	}
	return &replyError{fmt.Errorf("hypr: %s", abbrev(msg))}
}

// request sends one request on a fresh connection and returns the whole
// reply (Hyprland closes the connection after answering).
func request(path, req string) (string, error) {
	deadline := time.Now().Add(timeout)
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return "", fmt.Errorf("hypr: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return "", fmt.Errorf("hypr: %w", err)
	}
	// One write: Hyprland stops reading at the first short read.
	if _, err := io.WriteString(conn, req); err != nil {
		return "", fmt.Errorf("hypr: send: %w", err)
	}
	b, err := io.ReadAll(conn)
	if err != nil {
		return "", fmt.Errorf("hypr: reply: %w", err)
	}
	return string(b), nil
}

func abbrev(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
