package hypr

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHypr is a stand-in for Hyprland's control socket: one request per
// connection, answered by reply and then closed.
type fakeHypr struct {
	path  string
	reply func(req string) string

	mu   sync.Mutex
	reqs []string
}

func newFake(t *testing.T, path string, reply func(string) string) *fakeHypr {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeHypr{path: path, reply: reply}
	var wg sync.WaitGroup
	t.Cleanup(func() { l.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 8192)
			n, _ := c.Read(buf) // like Hyprland: one short read is the request
			req := string(buf[:n])
			f.mu.Lock()
			f.reqs = append(f.reqs, req)
			f.mu.Unlock()
			if r := f.reply(req); r != "" {
				c.Write([]byte(r))
			}
			c.Close()
		}
	}()
	return f
}

func (f *fakeHypr) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func clientsJSON(pid, fs, fsClient int, mapped bool) string {
	return fmt.Sprintf(`[{"address":"0x1","mapped":true,"hidden":false,"pid":1,"fullscreen":0,"fullscreenClient":0},
{"address":"0x2","mapped":%v,"hidden":false,"pid":%d,"fullscreen":%d,"fullscreenClient":%d}]`, mapped, pid, fs, fsClient)
}

// Replies of a Hyprland whose config is hyprlang (hyprland.conf) or Lua.
const (
	hyprlangRejectsLua = "Invalid dispatcher"
	luaRejectsHyprlang = "error: [string \"return hl.dispatch(focuswindow pid:4242)\"]:1: ')' expected near 'pid'\n\n → Note: dispatch in lua is a shorthand for hl.dispatch(...), your syntax might need to be updated.\n\n\n" +
		"error: [string \"return hl.dispatch(fullscreenstate 2 2)\"]:1: ')' expected near '2'\n\n → Note: dispatch in lua is a shorthand for hl.dispatch(...), your syntax might need to be updated."
)

// server answers j/clients with clients, Lua-syntax dispatches with lua
// and hyprlang batches with batch.
func server(clients, lua, batch string) func(string) string {
	return func(req string) string {
		switch {
		case req == "j/clients":
			return clients
		case strings.HasPrefix(req, "dispatch hl.dsp."):
			return lua
		case strings.HasPrefix(req, "[[BATCH]]"):
			return batch
		}
		return "unknown request"
	}
}

// resetForm forgets the remembered dispatch syntax for the test.
func resetForm(t *testing.T) {
	form.Store(formUnknown)
	t.Cleanup(func() { form.Store(formUnknown) })
}

func sockIn(t *testing.T) string {
	// Short path: unix socket paths are limited to ~108 bytes.
	dir, err := os.MkdirTemp("", "hy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func TestSocketPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	have := func(paths ...string) func(string) bool {
		return func(p string) bool { return slices.Contains(paths, p) }
	}
	const sig = "abc_123_456"
	xdg := "/run/user/1000/hypr/" + sig + "/.socket.sock"
	old := "/tmp/hypr/" + sig + "/.socket.sock"
	for _, c := range []struct {
		name   string
		env    map[string]string
		exists func(string) bool
		want   string
	}{
		{"xdg", map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": sig, "XDG_RUNTIME_DIR": "/run/user/1000"}, have(xdg), xdg},
		{"xdg preferred", map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": sig, "XDG_RUNTIME_DIR": "/run/user/1000"}, have(xdg, old), xdg},
		{"old tmp", map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": sig, "XDG_RUNTIME_DIR": "/run/user/1000"}, have(old), old},
		{"old tmp, no XDG_RUNTIME_DIR", map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": sig}, have(old), old},
		{"no signature", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, have(xdg, old, "/run/user/1000/hypr/.socket.sock"), ""},
		{"no socket", map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": sig, "XDG_RUNTIME_DIR": "/run/user/1000"}, have(), ""},
		{"bad signature", map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": "../x", "XDG_RUNTIME_DIR": "/run/user/1000"}, func(string) bool { return true }, ""},
	} {
		got, ok := socketPath(env(c.env), c.exists)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: socketPath = %q, %v; want %q", c.name, got, ok, c.want)
		}
	}
}

func TestAvailable(t *testing.T) {
	rt := t.TempDir()
	const sig = "testsig"
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	if Available() {
		t.Error("Available with no signature")
	}
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", sig)
	if Available() != exists(filepath.Join("/tmp/hypr", sig, ".socket.sock")) {
		t.Error("Available with no socket")
	}
	p := filepath.Join(rt, "hypr", sig, ".socket.sock")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !Available() {
		t.Error("not Available with signature and socket")
	}
}

func TestSetFullscreen(t *testing.T) {
	const pid = 4242
	luaOn := `dispatch hl.dsp.window.fullscreen_state({ internal = 2, client = 2, action = "set", window = "pid:4242" })`
	luaOff := `dispatch hl.dsp.window.fullscreen_state({ internal = 0, client = 0, action = "set", window = "pid:4242" })`
	batchOn := "[[BATCH]]dispatch focuswindow pid:4242;dispatch fullscreenstate 2 2"
	batchOff := "[[BATCH]]dispatch focuswindow pid:4242;dispatch fullscreenstate 0 0"
	for _, c := range []struct {
		name     string
		on       bool
		clients  string
		lua      string // reply to the Lua-syntax dispatch
		batch    string // reply to the hyprlang batch
		reqs     []string
		wantErr  string // "" = success, "nowin" = ErrNoWindow
		wantForm int32
	}{
		// Lua config: one dispatch, no focus change, no batch.
		{"lua enter", true, clientsJSON(pid, 0, 0, true), "ok", luaRejectsHyprlang, []string{luaOn}, "", formLua},
		{"lua leave", false, clientsJSON(pid, 2, 2, true), "ok\n", luaRejectsHyprlang, []string{luaOff}, "", formLua},
		// hyprlang config: Lua syntax rejected, then the batch.
		{"hyprlang enter", true, clientsJSON(pid, 0, 0, true), hyprlangRejectsLua, "ok\n\n\nok", []string{luaOn, batchOn}, "", formHyprlang},
		{"hyprlang leave", false, clientsJSON(pid, 2, 2, true), hyprlangRejectsLua, "ok\n\n\nok", []string{luaOff, batchOff}, "", formHyprlang},
		{"hyprlang enter from maximised", true, clientsJSON(pid, 1, 0, true), hyprlangRejectsLua, "ok\n\n\nok\n", []string{luaOn, batchOn}, "", formHyprlang},
		// Nothing to do / nothing to act on: only the clients query.
		{"already fullscreen", true, clientsJSON(pid, 2, 2, true), "ok", "ok\n\n\nok", nil, "", formUnknown},
		{"already windowed", false, clientsJSON(pid, 0, 0, true), "ok", "ok\n\n\nok", nil, "", formUnknown},
		{"not mapped yet", true, clientsJSON(pid, 0, 0, false), "ok", "ok\n\n\nok", nil, "nowin", formUnknown},
		{"no such pid", true, clientsJSON(pid+1, 0, 0, true), "ok", "ok\n\n\nok", nil, "nowin", formUnknown},
		{"no clients", true, "[]", "ok", "ok\n\n\nok", nil, "nowin", formUnknown},
		{"bad clients reply", true, "unknown request", "ok", "ok\n\n\nok", nil, "clients", formUnknown},
		// Failures.
		{"both rejected", true, clientsJSON(pid, 0, 0, true), "lua says no", "batch says no", []string{luaOn, batchOn}, "batch says no", formUnknown},
		{"focus fails", true, clientsJSON(pid, 0, 0, true), hyprlangRejectsLua, "No such window found\n\n\nok", []string{luaOn, batchOn}, "nowin", formUnknown},
		{"fullscreenstate fails", true, clientsJSON(pid, 0, 0, true), hyprlangRejectsLua, "ok\n\n\nWindow not found", []string{luaOn, batchOn}, "Window not found", formUnknown},
		{"lua config, lua error", true, clientsJSON(pid, 0, 0, true), "error: something", luaRejectsHyprlang, []string{luaOn, batchOn}, "expected near", formUnknown},
		{"one ok only", true, clientsJSON(pid, 0, 0, true), hyprlangRejectsLua, "ok", []string{luaOn, batchOn}, "hypr", formUnknown},
		{"empty replies", true, clientsJSON(pid, 0, 0, true), "", "", []string{luaOn, batchOn}, "hypr", formUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			resetForm(t)
			f := newFake(t, sockIn(t), server(c.clients, c.lua, c.batch))
			err := setFullscreen(f.path, pid, c.on)
			switch c.wantErr {
			case "":
				if err != nil {
					t.Fatalf("err = %v", err)
				}
			case "nowin":
				if !errors.Is(err, ErrNoWindow) {
					t.Fatalf("err = %v, want ErrNoWindow", err)
				}
			default:
				if err == nil || errors.Is(err, ErrNoWindow) || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want one mentioning %q", err, c.wantErr)
				}
			}
			want := append([]string{"j/clients"}, c.reqs...)
			if got := f.requests(); !slices.Equal(got, want) {
				t.Errorf("requests = %q, want %q", got, want)
			}
			if got := form.Load(); got != c.wantForm {
				t.Errorf("remembered form = %d, want %d", got, c.wantForm)
			}
		})
	}
}

// TestRememberedForm checks that once a syntax worked, later calls send
// only that one.
func TestRememberedForm(t *testing.T) {
	const pid = 4242
	for _, c := range []struct {
		name       string
		lua, batch string
		second     string // the only dispatch on the second call
	}{
		{"lua", "ok", luaRejectsHyprlang, `dispatch hl.dsp.window.fullscreen_state({ internal = 0, client = 0, action = "set", window = "pid:4242" })`},
		{"hyprlang", hyprlangRejectsLua, "ok\n\n\nok", "[[BATCH]]dispatch focuswindow pid:4242;dispatch fullscreenstate 0 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resetForm(t)
			state := clientsJSON(pid, 0, 0, true)
			var mu sync.Mutex
			f := newFake(t, sockIn(t), func(req string) string {
				mu.Lock()
				defer mu.Unlock()
				return server(state, c.lua, c.batch)(req)
			})
			if err := setFullscreen(f.path, pid, true); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			state = clientsJSON(pid, 2, 2, true)
			mu.Unlock()
			n := len(f.requests())
			if err := setFullscreen(f.path, pid, false); err != nil {
				t.Fatal(err)
			}
			want := []string{"j/clients", c.second}
			if got := f.requests()[n:]; !slices.Equal(got, want) {
				t.Errorf("second call sent %q, want %q", got, want)
			}

			// A remembered form that then fails is an error, not a retry
			// of the other syntax.
			mu.Lock()
			state = clientsJSON(pid, 0, 0, true)
			c.lua, c.batch = "nope", "nope"
			mu.Unlock()
			n = len(f.requests())
			if err := setFullscreen(f.path, pid, true); err == nil {
				t.Error("no error from a rejected dispatch")
			}
			if got := f.requests()[n:]; len(got) != 2 {
				t.Errorf("third call sent %q, want j/clients and one dispatch", got)
			}
		})
	}
}

func TestSetFullscreenEnv(t *testing.T) {
	// Through the public entry point: the socket is found via the environment.
	rt, err := os.MkdirTemp("", "hy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "sig")
	resetForm(t)
	f := newFake(t, filepath.Join(rt, "hypr", "sig", ".socket.sock"), server(clientsJSON(7, 0, 0, true), "ok", luaRejectsHyprlang))
	if !Available() {
		t.Fatal("not Available")
	}
	if err := SetFullscreen(7, true); err != nil {
		t.Fatal(err)
	}
	if got := f.requests(); len(got) != 2 || got[1] != `dispatch hl.dsp.window.fullscreen_state({ internal = 2, client = 2, action = "set", window = "pid:7" })` {
		t.Errorf("requests = %q", got)
	}

	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	if err := SetFullscreen(7, true); err == nil {
		t.Error("SetFullscreen without Hyprland succeeded")
	}
}

func TestNoServer(t *testing.T) {
	p := sockIn(t) // nothing listening
	if err := setFullscreen(p, 1, true); err == nil || errors.Is(err, ErrNoWindow) {
		t.Errorf("err = %v, want a connection error", err)
	}
}

func TestTimeout(t *testing.T) {
	old := timeout
	timeout = 100 * time.Millisecond
	t.Cleanup(func() { timeout = old })
	release := make(chan struct{})
	f := newFake(t, sockIn(t), func(string) string { <-release; return "" })
	defer close(release)
	start := time.Now()
	err := setFullscreen(f.path, 1, true)
	if err == nil {
		t.Fatal("no error from a server that never answers")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}
