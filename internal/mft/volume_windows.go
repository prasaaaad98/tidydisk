//go:build windows

package mft

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"tidydisk/internal/scan"
)

// ErrNeedsAdmin is returned when the raw volume cannot be opened.
var ErrNeedsAdmin = errors.New("reading the file table needs an administrator terminal")

// Options tunes the file table scan.
type Options struct {
	BigFileMin int64
	Progress   *atomic.Int64 // if set, counts file records as they are read
}

// CanRead reports whether this process may read the raw volume (admin).
func CanRead(vol string) bool {
	f, err := openVolume(strings.ToUpper(vol))
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Scan reads the whole file table of the volume that holds path and returns a
// tree of that entire volume. Use Result.Find(path) to get the part you want.
func Scan(path string, opt Options) (*scan.Result, error) {
	vol := strings.ToUpper(filepath.VolumeName(path))
	if len(vol) != 2 || vol[1] != ':' {
		return nil, fmt.Errorf("file table scan needs a drive-letter path, got %q", path)
	}
	start := time.Now()

	f, err := openVolume(vol)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	bootSector := make([]byte, 4096) // aligned for both 512-byte and 4K sectors
	if _, err := f.ReadAt(bootSector, 0); err != nil {
		return nil, fmt.Errorf("reading boot sector of %s: %w", vol, err)
	}
	boot, err := parseBoot(bootSector)
	if err != nil {
		return nil, err
	}
	cluster := int64(boot.clusterSize)
	rs := int64(boot.recordSize)

	// Record 0 is the file table's own record; its $DATA says where the rest is.
	first := make([]byte, max(rs, cluster))
	if _, err := f.ReadAt(first, boot.mftLCN*cluster); err != nil {
		return nil, fmt.Errorf("reading file table record 0: %w", err)
	}
	rec0 := first[:rs]
	if err := applyFixup(rec0); err != nil {
		return nil, err
	}
	r0, err := parseRecord(rec0, true)
	if err != nil {
		return nil, err
	}
	if !r0.hasUnnamedData || len(r0.unnamedRuns) == 0 {
		return nil, errors.New("mft: record 0 has no data runs")
	}
	if r0.hasAttrList {
		// The table is so fragmented that some of its pieces are listed in
		// other records. We read the pieces we know about and say so.
		fmt.Fprintln(os.Stderr, "note: the file table is heavily fragmented; some records may be missed")
	}

	nRecords := r0.unnamedSize / rs
	t := newTable(nRecords)
	buf := make([]byte, 4<<20) // 4 MB per read; a multiple of every cluster and record size
	var recNo int64
	for _, rn := range r0.unnamedRuns {
		runBytes := rn.length * cluster
		if runBytes%rs != 0 {
			return nil, errors.New("mft: unsupported file table layout (record split across pieces)")
		}
		if rn.sparse {
			recNo += runBytes / rs
			continue
		}
		pos := rn.lcn * cluster
		for done := int64(0); done < runBytes && recNo < nRecords; {
			n := min(int64(len(buf)), runBytes-done)
			if _, err := f.ReadAt(buf[:n], pos+done); err != nil && !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("reading file table: %w", err)
			}
			before := recNo
			for o := int64(0); o+rs <= n && recNo < nRecords; o += rs {
				t.add(recNo, buf[o:o+rs])
				recNo++
			}
			if opt.Progress != nil {
				opt.Progress.Add(recNo - before)
			}
			done += n
		}
	}

	res := t.build(vol+`\`, opt.BigFileMin)
	res.Elapsed = time.Since(start)
	return res, nil
}

func openVolume(vol string) (*os.File, error) {
	name := `\\.\` + vol
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return nil, ErrNeedsAdmin
		}
		return nil, fmt.Errorf("opening %s: %w", name, err)
	}
	return os.NewFile(uintptr(h), name), nil
}
