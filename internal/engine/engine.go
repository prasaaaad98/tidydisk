//go:build windows

// Package engine picks a scan engine and runs it. The CLI and the GUI share it.
package engine

import (
	"errors"
	"fmt"
	"sync/atomic"

	"tidydisk/internal/mft"
	"tidydisk/internal/scan"
)

// Options for Run.
type Options struct {
	BigFileMin int64
	Progress   *atomic.Int64
	// OnFallback is called when "auto" can't use the file table and walks instead.
	OnFallback func(reason error)
}

// Run scans path with "walk", "mft" or "auto" and returns the result plus the
// index of path inside it. The mft engine always scans the whole volume.
func Run(name, path string, opt Options) (*scan.Result, int32, error) {
	switch name {
	case "walk":
		r, err := scan.Walk(path, scan.Options{BigFileMin: opt.BigFileMin, Progress: opt.Progress})
		return r, 0, err
	case "mft":
		r, err := mft.Scan(path, mft.Options{BigFileMin: opt.BigFileMin, Progress: opt.Progress})
		if err != nil {
			return nil, 0, err
		}
		i, ok := r.Find(path)
		if !ok {
			return nil, 0, fmt.Errorf("%s was not found in the file table", path)
		}
		return r, i, nil
	case "auto":
		r, i, err := Run("mft", path, opt)
		if err == nil {
			return r, i, nil
		}
		if errors.Is(err, mft.ErrNeedsAdmin) || errors.Is(err, mft.ErrNotNTFS) {
			if opt.OnFallback != nil {
				opt.OnFallback(err)
			}
			return Run("walk", path, opt)
		}
		return nil, 0, err
	}
	return nil, 0, fmt.Errorf("unknown engine %q (use walk, mft or auto)", name)
}
