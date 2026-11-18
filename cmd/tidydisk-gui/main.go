//go:build windows

// Command tidydisk-gui is the desktop window for tidydisk. It hosts a single
// HTML page in WebView2 (built into Windows 11) and runs scans in the
// background. Build with -ldflags "-H windowsgui" so no console opens.
package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"

	"tidydisk/internal/catalog"
	"tidydisk/internal/engine"
	"tidydisk/internal/report"
	"tidydisk/internal/scan"
	"tidydisk/internal/winsys"
)

//go:embed ui.html
var page string

const version = "0.2.0"

// The window must stay on the thread that created it.
func init() { runtime.LockOSThread() }

func main() {
	entries, err := catalog.Builtin()
	if err != nil {
		fatal("The built-in catalog is broken: " + err.Error())
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		Debug:     os.Getenv("TIDYDISK_DEBUG") != "",
		DataPath:  filepath.Join(os.Getenv("LOCALAPPDATA"), "tidydisk", "webview"),
		WindowOptions: webview2.WindowOptions{
			Title: "tidydisk", Width: 940, Height: 780, Center: true,
		},
	})
	if w == nil {
		fatal("tidydisk needs the Microsoft Edge WebView2 Runtime, which is part of Windows 11.\nInstall it from Microsoft and try again.")
	}
	defer w.Destroy()
	w.SetSize(640, 520, webview2.HintMin)

	a := &app{w: w, entries: entries}
	must(w.Bind("tdInfo", a.info))
	must(w.Bind("tdAnalyze", a.analyze))
	must(w.Bind("tdReveal", winsys.ShowInExplorer))
	must(w.Bind("tdRelaunchAdmin", a.relaunchAdmin))
	w.SetHtml(page)
	w.Run()
}

type app struct {
	w       webview2.WebView
	entries []catalog.Entry
	busy    atomic.Bool
}

type info struct {
	Version  string         `json:"version"`
	Elevated bool           `json:"elevated"`
	Drives   []winsys.Drive `json:"drives"`
	Groups   []string       `json:"groups"`
}

func (a *app) info() info {
	return info{Version: version, Elevated: winsys.IsElevated(), Drives: winsys.Drives(), Groups: catalog.Groups}
}

type result struct {
	Drive        winsys.Drive     `json:"drive"`
	Engine       string           `json:"engine"`
	Fallback     string           `json:"fallback,omitempty"`
	ElapsedMs    int64            `json:"elapsedMs"`
	ScannedFiles int64            `json:"scannedFiles"`
	ScannedBytes int64            `json:"scannedBytes"`
	Findings     []report.Finding `json:"findings"`
	Unknown      []report.Hotspot `json:"unknown"`
	Files        []scan.File      `json:"files"`
}

const unknownMin = 500 << 20 // folders and files the catalog doesn't know, from 500 MB

// analyze starts a scan and returns at once; progress and the result arrive
// in the page through window.tidy.on(kind, data).
func (a *app) analyze(drive string) error {
	if !a.busy.CompareAndSwap(false, true) {
		return errors.New("a scan is already running")
	}
	go func() {
		defer a.busy.Store(false)
		start := time.Now()
		var count atomic.Int64
		var phase atomic.Value
		phase.Store("Scanning files")
		done := make(chan struct{})
		go func() {
			t := time.NewTicker(200 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					a.emit("progress", map[string]any{"files": count.Load(), "phase": phase.Load()})
				}
			}
		}()
		defer close(done)

		var fallback string
		r, root, err := engine.Run("auto", drive, engine.Options{
			BigFileMin: unknownMin,
			Progress:   &count,
			OnFallback: func(err error) { fallback = err.Error() },
		})
		if err != nil {
			a.emit("error", err.Error())
			return
		}

		phase.Store("Matching known caches")
		findings := report.Collect(a.entries, report.Options{RunResolvers: true, Tree: r, Under: drive})
		covered := make([]string, len(findings))
		for i, f := range findings {
			covered[i] = f.Path
		}
		d, _ := winsys.DriveInfo(drive)
		a.emit("result", result{
			Drive:        d,
			Engine:       r.Engine,
			Fallback:     fallback,
			ElapsedMs:    time.Since(start).Milliseconds(),
			ScannedFiles: r.Dirs[root].TotalFiles,
			ScannedBytes: r.Dirs[root].TotalBytes,
			Findings:     findings,
			Unknown:      report.Hotspots(r, root, covered, unknownMin, 12),
			Files:        report.LargeFiles(r, r.Path(root), covered, 12),
		})
	}()
	return nil
}

func (a *app) relaunchAdmin() error {
	if err := winsys.RelaunchAsAdmin(); err != nil {
		return errors.New("Windows did not start tidydisk as administrator")
	}
	a.w.Terminate()
	return nil
}

// emit sends an event to the page from any goroutine.
func (a *app) emit(kind string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	k, _ := json.Marshal(kind)
	js := "window.tidy && window.tidy.on(" + string(k) + "," + string(b) + ")"
	a.w.Dispatch(func() { a.w.Eval(js) })
}

func must(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(msg string) {
	t, _ := syscall.UTF16PtrFromString("tidydisk")
	m, _ := syscall.UTF16PtrFromString(msg)
	const mbIconError = 0x10
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbIconError)
	os.Exit(1)
}
