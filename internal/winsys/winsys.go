//go:build windows

// Package winsys wraps the few Windows calls the GUI needs.
package winsys

import (
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Drive is one fixed local drive.
type Drive struct {
	Root       string `json:"root"` // "C:\"
	Label      string `json:"label"`
	FileSystem string `json:"fileSystem"`
	Total      int64  `json:"total"`
	Free       int64  `json:"free"`
	System     bool   `json:"system"` // holds Windows
}

// Drives lists fixed (non-removable, non-network) drives.
func Drives() []Drive {
	var out []Drive
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return out
	}
	sysDrive := strings.ToUpper(os.Getenv("SystemDrive")) + `\`
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(p) != windows.DRIVE_FIXED {
			continue
		}
		d, err := DriveInfo(root)
		if err != nil {
			continue
		}
		d.System = root == sysDrive
		out = append(out, d)
	}
	return out
}

// DriveInfo returns size, free space, label and file system of a drive.
func DriveInfo(root string) (Drive, error) {
	d := Drive{Root: root}
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return d, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return d, err
	}
	d.Total, d.Free = int64(total), int64(free)
	label := make([]uint16, windows.MAX_PATH+1)
	fs := make([]uint16, windows.MAX_PATH+1)
	if windows.GetVolumeInformation(p, &label[0], uint32(len(label)), nil, nil, nil, &fs[0], uint32(len(fs))) == nil {
		d.Label = windows.UTF16ToString(label)
		d.FileSystem = windows.UTF16ToString(fs)
	}
	return d, nil
}

// IsElevated reports whether the process runs as administrator.
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// RelaunchAsAdmin starts this program again through the UAC prompt.
// It returns an error if the user says no.
func RelaunchAsAdmin() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// ShowInExplorer opens Explorer with path selected.
func ShowInExplorer(path string) error {
	cmd := exec.Command("explorer.exe")
	// Explorer parses its own command line; pass it exactly.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	return cmd.Start()
}
