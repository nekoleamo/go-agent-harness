module github.com/nekoleamo/go-agent-harness

go 1.27.0

require (
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20261008173134-6b8d4baf91b4 // indirect
	github.com/charmbracelet/x/ansi v0.11.9 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
)

require (
	charm.land/bubbletea/v2 v2.1.1
	charm.land/lipgloss/v2 v2.0.6
	github.com/Detective-XH/gopdf v0.8.7
	github.com/creack/pty v1.1.24
	github.com/fsnotify/fsnotify v1.10.1
	github.com/hashicorp/go-version v1.9.0
	github.com/klauspost/compress v1.20.3
	github.com/mattn/go-runewidth v0.0.31
	github.com/nekoleamo/go-agent-harness/sdk v0.0.0
	github.com/tetratelabs/wazero v1.12.0
	github.com/yuin/goldmark v1.8.7
	go.starlark.net v0.0.0-20261021141710-4f4267fb140d
	golang.org/x/image v0.47.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/nekoleamo/go-agent-harness/sdk => ./sdk
