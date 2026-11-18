//go:build windows

package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Options tunes the folder walker.
type Options struct {
	Workers    int   // folders read at the same time; 0 = 4 x CPU count
	BigFileMin int64 // files at least this big are listed in Result.BigFiles; 0 = none
}

// Walk measures root by listing folders in parallel through the normal
// Windows file APIs. It works without admin rights, but it never follows
// junctions or symlinks, so it cannot count a folder twice or wander outside
// root. Cloud placeholder folders (OneDrive) are real folders and are followed.
func Walk(root string, opt Options) (*Result, error) {
	root = normalizeRoot(root)
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", root)
	}
	if opt.Workers <= 0 {
		opt.Workers = runtime.NumCPU() * 4 // folder listing waits on the disk, not the CPU
	}
	start := time.Now()
	w := &walker{opt: opt, sem: make(chan struct{}, opt.Workers), res: &Result{Engine: "walk", Root: root}}
	w.res.Dirs = append(w.res.Dirs, Dir{Name: root, Parent: -1})
	w.wg.Add(1)
	go w.visit(0, root, 0)
	w.wg.Wait()
	w.res.Elapsed = time.Since(start)
	w.res.Finalize()
	return w.res, nil
}

func normalizeRoot(p string) string {
	if len(p) == 2 && p[1] == ':' {
		return p + `\`
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

type walker struct {
	opt Options
	sem chan struct{}
	wg  sync.WaitGroup
	mu  sync.Mutex
	res *Result
}

func (w *walker) visit(id int32, path string, depth int32) {
	defer w.wg.Done()

	w.sem <- struct{}{}
	entries, readErr := readDir(path)
	var (
		files, bytes int64
		subdirs      []string
		big          []File
		skipped      []string
	)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue // removed while we were looking
		}
		attrs := fileAttributes(info)
		if attrs&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
			if attrs&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 && !isCloudFolder(filepath.Join(path, e.Name())) {
				skipped = append(skipped, filepath.Join(path, e.Name()))
				continue
			}
			subdirs = append(subdirs, e.Name())
			continue
		}
		files++
		bytes += info.Size()
		if w.opt.BigFileMin > 0 && info.Size() >= w.opt.BigFileMin {
			big = append(big, File{Path: filepath.Join(path, e.Name()), Size: info.Size()})
		}
	}
	<-w.sem

	w.mu.Lock()
	r := w.res
	if readErr != nil {
		r.Errors++
		addSample(&r.ErrorSamples, fmt.Sprintf("%s: %v", path, readErr))
	}
	r.Dirs[id].FileCount = files
	r.Dirs[id].FileBytes = bytes
	r.BigFiles = append(r.BigFiles, big...)
	r.SkippedLinks += int64(len(skipped))
	for _, s := range skipped {
		addSample(&r.SkippedSamples, s)
	}
	ids := make([]int32, len(subdirs))
	for i, name := range subdirs {
		ids[i] = int32(len(r.Dirs))
		r.Dirs = append(r.Dirs, Dir{Name: name, Parent: id, Depth: depth + 1})
	}
	w.mu.Unlock()

	for i, name := range subdirs {
		w.wg.Add(1)
		go w.visit(ids[i], filepath.Join(path, name), depth+1)
	}
}

// readDir lists a folder without sorting (os.ReadDir sorts, which we don't need).
func readDir(path string) ([]os.DirEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func fileAttributes(info os.FileInfo) uint32 {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes
	}
	var a uint32
	if info.IsDir() {
		a |= syscall.FILE_ATTRIBUTE_DIRECTORY
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		a |= syscall.FILE_ATTRIBUTE_REPARSE_POINT
	}
	return a
}

// isCloudFolder reports whether a reparse-point folder is a cloud-files
// placeholder (OneDrive and similar). Those hold real content and are safe to
// enter, unlike junctions and symlinks.
func isCloudFolder(path string) bool {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	var fd syscall.Win32finddata
	h, err := syscall.FindFirstFile(p, &fd)
	if err != nil {
		return false
	}
	syscall.FindClose(h)
	const cloudTag, cloudMask = 0x9000001A, 0xFFFF0FFF // IO_REPARSE_TAG_CLOUD .. CLOUD_F
	return fd.Reserved0&cloudMask == cloudTag
}
