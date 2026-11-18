package mft

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// --- helpers that build NTFS structures by hand -------------------------

const testRecSize = 1024

func align8(n int) int { return (n + 7) &^ 7 }

func fileNameAttr(parent uint64, ns uint8, name string) []byte {
	u := utf16.Encode([]rune(name))
	vlen := 0x42 + 2*len(u)
	a := make([]byte, align8(0x18+vlen))
	le.PutUint32(a[0:], attrFileName)
	le.PutUint32(a[4:], uint32(len(a)))
	le.PutUint32(a[0x10:], uint32(vlen))
	le.PutUint16(a[0x14:], 0x18)
	v := a[0x18:]
	le.PutUint64(v[0:], parent|1<<48) // sequence number in the top bits must be ignored
	v[0x40] = byte(len(u))
	v[0x41] = ns
	for i, c := range u {
		le.PutUint16(v[0x42+2*i:], c)
	}
	return a
}

func residentDataAttr(name string, value []byte) []byte {
	nameU := utf16.Encode([]rune(name))
	nameOff := 0x18
	valOff := align8(nameOff + 2*len(nameU))
	a := make([]byte, align8(valOff+len(value)))
	le.PutUint32(a[0:], attrData)
	le.PutUint32(a[4:], uint32(len(a)))
	a[9] = byte(len(nameU))
	le.PutUint16(a[0x0A:], uint16(nameOff))
	for i, c := range nameU {
		le.PutUint16(a[nameOff+2*i:], c)
	}
	le.PutUint32(a[0x10:], uint32(len(value)))
	le.PutUint16(a[0x14:], uint16(valOff))
	copy(a[valOff:], value)
	return a
}

func nonResidentDataAttr(realSize int64, flags uint16, onDisk int64, runs []byte) []byte {
	hdr := 0x40
	if flags != 0 {
		hdr = 0x48
	}
	a := make([]byte, align8(hdr+len(runs)))
	le.PutUint32(a[0:], attrData)
	le.PutUint32(a[4:], uint32(len(a)))
	a[8] = 1
	le.PutUint16(a[0x0C:], flags)
	le.PutUint16(a[0x20:], uint16(hdr))
	le.PutUint64(a[0x28:], uint64(realSize))
	le.PutUint64(a[0x30:], uint64(realSize))
	le.PutUint64(a[0x38:], uint64(realSize))
	if flags != 0 {
		le.PutUint64(a[0x40:], uint64(onDisk))
	}
	copy(a[hdr:], runs)
	return a
}

// buildRecord lays out a file record and applies the on-disk fixup, so the
// parser has to undo it exactly like it would for a real disk.
func buildRecord(t *testing.T, flags uint16, base uint64, attrs ...[]byte) []byte {
	t.Helper()
	rec := make([]byte, testRecSize)
	copy(rec, "FILE")
	const usOff, usCnt = 0x30, testRecSize/512 + 1
	le.PutUint16(rec[4:], usOff)
	le.PutUint16(rec[6:], usCnt)
	le.PutUint16(rec[0x16:], flags)
	le.PutUint64(rec[0x20:], base)
	off := align8(usOff + 2*usCnt)
	le.PutUint16(rec[0x14:], uint16(off))
	for _, a := range attrs {
		copy(rec[off:], a)
		off += len(a)
	}
	le.PutUint32(rec[off:], attrEnd)
	off += 8
	if off > testRecSize {
		t.Fatalf("attributes do not fit in a record")
	}
	le.PutUint32(rec[0x18:], uint32(off))
	le.PutUint32(rec[0x1C:], testRecSize)

	le.PutUint16(rec[usOff:], 0x0007) // update sequence number
	for i := 1; i < usCnt; i++ {
		end := i*512 - 2
		copy(rec[usOff+2*i:], rec[end:end+2])
		le.PutUint16(rec[end:], 0x0007)
	}
	return rec
}

const (
	flagInUse = 0x01
	flagDir   = 0x02
)

// --- tests -----------------------------------------------------------------

func TestParseBoot(t *testing.T) {
	b := make([]byte, 512)
	copy(b[3:], "NTFS    ")
	le.PutUint16(b[0x0B:], 512)
	b[0x0D] = 8
	le.PutUint64(b[0x30:], 786432)
	b[0x40] = 0xF6 // -10 => 1024-byte records
	info, err := parseBoot(b)
	if err != nil {
		t.Fatal(err)
	}
	if info.clusterSize != 4096 || info.recordSize != 1024 || info.mftLCN != 786432 {
		t.Fatalf("got %+v", info)
	}

	copy(b[3:], "EXFAT   ")
	if _, err := parseBoot(b); err != ErrNotNTFS {
		t.Fatalf("want ErrNotNTFS, got %v", err)
	}
}

func TestParseRuns(t *testing.T) {
	// len 0x18 at 0x5634; len 0x30 at -16 from there; sparse run of 5; end
	b := []byte{0x21, 0x18, 0x34, 0x56, 0x11, 0x30, 0xF0, 0x01, 0x05, 0x00}
	runs, err := parseRuns(b)
	if err != nil {
		t.Fatal(err)
	}
	want := []run{{lcn: 0x5634, length: 0x18}, {lcn: 0x5624, length: 0x30}, {length: 5, sparse: true}}
	if len(runs) != len(want) {
		t.Fatalf("got %+v", runs)
	}
	for i := range want {
		if runs[i] != want[i] {
			t.Fatalf("run %d: got %+v want %+v", i, runs[i], want[i])
		}
	}
	if _, err := parseRuns([]byte{0x21, 0x18}); err == nil {
		t.Fatal("truncated run list should fail")
	}
}

func TestFixupAndLongName(t *testing.T) {
	// A 250-character name crosses the first 512-byte boundary, so it only
	// decodes correctly if the fixup restored the swapped bytes.
	long := strings.Repeat("a", 120) + "XY" + strings.Repeat("b", 128)
	rec := buildRecord(t, flagInUse, 0, fileNameAttr(5, nsWin32, long))
	if err := applyFixup(rec); err != nil {
		t.Fatal(err)
	}
	r, err := parseRecord(rec, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.names) != 1 || r.names[0].name != long || r.names[0].parent != 5 {
		t.Fatalf("name not restored: %+v", r.names)
	}
}

func TestFixupDetectsTornWrite(t *testing.T) {
	rec := buildRecord(t, flagInUse, 0, fileNameAttr(5, nsWin32, "x"))
	rec[1022] ^= 0xFF
	if err := applyFixup(rec); err == nil {
		t.Fatal("expected a torn-write error")
	}
}

func TestDataSizes(t *testing.T) {
	rec := buildRecord(t, flagInUse, 0,
		fileNameAttr(5, nsWin32, "f.bin"),
		nonResidentDataAttr(1<<20, 0, 0, []byte{0x11, 0x01, 0x10, 0}),
		residentDataAttr("Zone.Identifier", make([]byte, 26)),
	)
	if err := applyFixup(rec); err != nil {
		t.Fatal(err)
	}
	r, err := parseRecord(rec, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.dataBytes != 1<<20+26 {
		t.Fatalf("dataBytes = %d", r.dataBytes)
	}
	if !r.hasUnnamedData || r.unnamedSize != 1<<20 || len(r.unnamedRuns) != 1 || r.unnamedRuns[0].lcn != 0x10 {
		t.Fatalf("unnamed data wrong: %+v", r)
	}

	// A sparse 1 GB file that only uses 4 KB counts as 4 KB.
	sparse := buildRecord(t, flagInUse, 0, nonResidentDataAttr(1<<30, attrFlagSparse, 4096, []byte{0}))
	applyFixup(sparse)
	rs, _ := parseRecord(sparse, false)
	if rs.dataBytes != 4096 {
		t.Fatalf("sparse dataBytes = %d", rs.dataBytes)
	}
}

func TestTableBuild(t *testing.T) {
	tab := newTable(40)
	add := func(no int64, rec []byte) { tab.add(no, rec) }

	add(rootRecord, buildRecord(t, flagInUse|flagDir, 0, fileNameAttr(rootRecord, nsWin32, ".")))
	add(30, buildRecord(t, flagInUse|flagDir, 0, fileNameAttr(rootRecord, nsWin32, "Users")))
	add(31, buildRecord(t, flagInUse, 0, fileNameAttr(30, nsWin32, "a.bin"),
		nonResidentDataAttr(1000, 0, 0, []byte{0x11, 0x01, 0x10, 0})))
	// extension record of 31 carrying an extra stream
	add(32, buildRecord(t, flagInUse, 31, residentDataAttr("extra", make([]byte, 24))))
	// DOS name first, long name second: the long one must win
	add(33, buildRecord(t, flagInUse, 0, fileNameAttr(30, nsDOS, "LONGNA~1.TXT"),
		fileNameAttr(30, nsWin32, "long name.txt"), residentDataAttr("", make([]byte, 10))))
	// deleted file must be ignored
	add(34, buildRecord(t, 0, 0, fileNameAttr(30, nsWin32, "gone.bin"), residentDataAttr("", make([]byte, 99))))
	// file whose parent is not a folder we know
	add(35, buildRecord(t, flagInUse, 0, fileNameAttr(39, nsWin32, "orphan"), residentDataAttr("", make([]byte, 7))))

	res := tab.build(`C:\`, 500)
	if len(res.Dirs) != 2 {
		t.Fatalf("dirs = %+v", res.Dirs)
	}
	users, ok := res.Find(`c:\users`)
	if !ok {
		t.Fatal("Users not found")
	}
	d := res.Dirs[users]
	if d.FileCount != 2 || d.FileBytes != 1000+24+10 {
		t.Fatalf("Users = %+v", d)
	}
	if res.Dirs[0].TotalBytes != 1034 || res.Unreachable != 7 {
		t.Fatalf("root total %d, unreachable %d", res.Dirs[0].TotalBytes, res.Unreachable)
	}
	if len(res.BigFiles) != 1 || res.BigFiles[0].Path != `C:\Users\a.bin` || res.BigFiles[0].Size != 1024 {
		t.Fatalf("big files = %+v", res.BigFiles)
	}
	if tab.e[33].name != "long name.txt" {
		t.Fatalf("picked name %q", tab.e[33].name)
	}
}
