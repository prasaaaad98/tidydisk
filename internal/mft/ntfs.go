// Package mft measures an NTFS volume by reading its Master File Table (the
// "file table") directly, the way WizTree does. One sequential read of the
// table gives every file's name, parent folder and size, which is much faster
// than asking Windows to list every folder. It needs admin rights because it
// opens the raw volume.
//
// This file holds the pure parsing code (no Windows calls) so it can be tested
// with hand-built records.
package mft

import (
	"encoding/binary"
	"errors"
	"unicode/utf16"
)

var (
	// ErrNotNTFS means the volume is not formatted NTFS (FAT32, exFAT, ReFS...).
	ErrNotNTFS   = errors.New("volume is not NTFS")
	errBadBoot   = errors.New("mft: unreadable NTFS boot sector")
	errBadRecord = errors.New("mft: corrupt file record")
	errBadRuns   = errors.New("mft: corrupt data run list")
)

const (
	refMask = 0x0000FFFFFFFFFFFF // low 48 bits of a file reference = record number

	attrAttributeList = 0x20
	attrFileName      = 0x30
	attrData          = 0x80
	attrEnd           = 0xFFFFFFFF

	nsPOSIX = 0
	nsWin32 = 1
	nsDOS   = 2 // 8.3 short name; only used when there is no long name

	attrFlagCompressed = 0x0001
	attrFlagSparse     = 0x8000
)

var le = binary.LittleEndian

// bootInfo is what we need from the NTFS boot sector.
type bootInfo struct {
	bytesPerSector uint32
	clusterSize    uint32
	mftLCN         int64 // first cluster of the file table
	recordSize     uint32
}

func parseBoot(b []byte) (bootInfo, error) {
	if len(b) < 512 {
		return bootInfo{}, errBadBoot
	}
	if string(b[3:11]) != "NTFS    " {
		return bootInfo{}, ErrNotNTFS
	}
	bps := uint32(le.Uint16(b[0x0B:]))
	spcRaw := b[0x0D]
	spc := uint32(spcRaw)
	if spcRaw > 0x80 { // clusters over 64 KB are stored as a negative power of two
		spc = 1 << (256 - uint32(spcRaw))
	}
	info := bootInfo{bytesPerSector: bps, clusterSize: bps * spc, mftLCN: int64(le.Uint64(b[0x30:]))}
	cpr := int8(b[0x40])
	if cpr < 0 {
		info.recordSize = 1 << uint32(-cpr)
	} else {
		info.recordSize = uint32(cpr) * info.clusterSize
	}
	switch {
	case bps != 512 && bps != 1024 && bps != 2048 && bps != 4096,
		info.clusterSize == 0,
		info.recordSize < 512 || info.recordSize > 65536 || info.recordSize&(info.recordSize-1) != 0,
		info.mftLCN <= 0:
		return bootInfo{}, errBadBoot
	}
	return info, nil
}

// run is one contiguous piece of an attribute's data on disk, in clusters.
type run struct {
	lcn    int64
	length int64
	sparse bool
}

// parseRuns decodes an NTFS mapping-pairs list. Each run's start is stored
// relative to the previous one.
func parseRuns(b []byte) ([]run, error) {
	var runs []run
	var lcn int64
	i := 0
	for i < len(b) {
		h := b[i]
		if h == 0 {
			return runs, nil
		}
		lsz, osz := int(h&0x0F), int(h>>4)
		i++
		if lsz == 0 || lsz > 8 || osz > 8 || i+lsz+osz > len(b) {
			return nil, errBadRuns
		}
		var length int64
		for k := lsz - 1; k >= 0; k-- {
			length = length<<8 | int64(b[i+k])
		}
		i += lsz
		if osz == 0 {
			runs = append(runs, run{length: length, sparse: true})
			continue
		}
		var off int64
		for k := osz - 1; k >= 0; k-- {
			off = off<<8 | int64(b[i+k])
		}
		if osz < 8 && b[i+osz-1]&0x80 != 0 {
			off -= 1 << (8 * uint(osz)) // sign-extend
		}
		i += osz
		lcn += off
		runs = append(runs, run{lcn: lcn, length: length})
	}
	return runs, nil
}

// applyFixup undoes NTFS's torn-write protection. The last two bytes of every
// 512-byte stride are swapped out for a sequence number on disk; the real bytes
// live in the update sequence array.
func applyFixup(rec []byte) error {
	if len(rec) < 0x30 || string(rec[0:4]) != "FILE" {
		return errBadRecord
	}
	usOff := int(le.Uint16(rec[4:]))
	usCnt := int(le.Uint16(rec[6:]))
	if usCnt < 2 || usOff+2*usCnt > len(rec) {
		return errBadRecord
	}
	stride := len(rec) / (usCnt - 1)
	if stride*(usCnt-1) != len(rec) || stride < 256 {
		return errBadRecord
	}
	u0, u1 := rec[usOff], rec[usOff+1]
	for i := 1; i < usCnt; i++ {
		end := i*stride - 2
		if rec[end] != u0 || rec[end+1] != u1 {
			return errBadRecord // record was half-written
		}
		rec[end], rec[end+1] = rec[usOff+2*i], rec[usOff+2*i+1]
	}
	return nil
}

type fileName struct {
	parent    uint64
	namespace uint8
	name      string
}

// record is what one file record contributes.
type record struct {
	inUse, isDir bool
	base         uint64 // non-zero when this is an extension of another record
	names        []fileName
	hasAttrList  bool
	dataBytes    int64 // all data streams; on-disk size for sparse/compressed ones

	// unnamed $DATA details, used for record 0 ($MFT itself)
	hasUnnamedData bool
	unnamedSize    int64
	unnamedRuns    []run
}

// parseRecord reads the attributes of one fixed-up file record.
func parseRecord(rec []byte, wantRuns bool) (record, error) {
	var r record
	flags := le.Uint16(rec[0x16:])
	r.inUse = flags&0x01 != 0
	r.isDir = flags&0x02 != 0
	r.base = le.Uint64(rec[0x20:]) & refMask
	if !r.inUse {
		return r, nil
	}
	used := int(le.Uint32(rec[0x18:]))
	if used > len(rec) {
		used = len(rec)
	}
	off := int(le.Uint16(rec[0x14:]))
	for off+8 <= used {
		typ := le.Uint32(rec[off:])
		if typ == attrEnd {
			break
		}
		alen := int(le.Uint32(rec[off+4:]))
		if alen < 0x18 || off+alen > used {
			return r, errBadRecord
		}
		a := rec[off : off+alen]
		nonResident := a[8] != 0
		named := a[9] != 0

		switch typ {
		case attrAttributeList:
			r.hasAttrList = true

		case attrFileName:
			if nonResident {
				break
			}
			v, ok := residentValue(a)
			if !ok || len(v) < 0x42 {
				break
			}
			n := int(v[0x40])
			if 0x42+2*n > len(v) {
				break
			}
			r.names = append(r.names, fileName{
				parent:    le.Uint64(v) & refMask,
				namespace: v[0x41],
				name:      decodeUTF16(v[0x42 : 0x42+2*n]),
			})

		case attrData:
			if !nonResident {
				v, ok := residentValue(a)
				if ok {
					r.dataBytes += int64(len(v))
					if !named {
						r.hasUnnamedData, r.unnamedSize = true, int64(len(v))
					}
				}
				break
			}
			if alen < 0x40 {
				return r, errBadRecord
			}
			// Only the piece starting at VCN 0 carries the stream's sizes.
			if le.Uint64(a[0x10:]) == 0 {
				size := int64(le.Uint64(a[0x30:])) // logical size
				if le.Uint16(a[0x0C:])&(attrFlagSparse|attrFlagCompressed) != 0 && alen >= 0x48 {
					size = int64(le.Uint64(a[0x40:])) // what it really takes on disk
				}
				r.dataBytes += size
				if !named {
					r.hasUnnamedData, r.unnamedSize = true, int64(le.Uint64(a[0x30:]))
				}
			}
			if wantRuns && !named {
				ro := int(le.Uint16(a[0x20:]))
				if ro < alen {
					runs, err := parseRuns(a[ro:])
					if err != nil {
						return r, err
					}
					r.unnamedRuns = append(r.unnamedRuns, runs...)
				}
			}
		}
		off += alen
	}
	return r, nil
}

func residentValue(a []byte) ([]byte, bool) {
	vl := int(le.Uint32(a[0x10:]))
	vo := int(le.Uint16(a[0x14:]))
	if vo+vl > len(a) {
		return nil, false
	}
	return a[vo : vo+vl], true
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = le.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// bestName picks the long name over the 8.3 DOS name. For hard links the
// first long name wins and the file is counted once.
func bestName(names []fileName) (fileName, bool) {
	for _, n := range names {
		if n.namespace != nsDOS {
			return n, true
		}
	}
	if len(names) > 0 {
		return names[0], true
	}
	return fileName{}, false
}
