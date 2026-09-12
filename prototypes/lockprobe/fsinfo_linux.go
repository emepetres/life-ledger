//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Filesystem classes, in the only terms this probe cares about.
const (
	classLocal     = "local"     // persistent block device — the known-good case
	classNetwork   = "network"   // may accept lock syscalls without enforcing them
	classEphemeral = "ephemeral" // locks fine, loses the data — fatal for a different reason
	classForeign   = "foreign"   // honours locks, but via a non-POSIX server (dev-parity trap)
	classUnknown   = "unknown"
)

type fsKind struct {
	name  string
	class string
}

// fsTypes maps statfs f_type magics to the classification that matters here.
// Two distinct dangers share this table: a NETWORK filesystem may accept the
// lock syscall without enforcing it (ADR-0003, Azure Files SMB), while an
// EPHEMERAL one enforces locks perfectly and then throws the database away.
// Every lock-level check in this probe passes on tmpfs.
var fsTypes = map[int64]fsKind{
	0xEF53:     {"ext2/3/4", classLocal},
	0x58465342: {"xfs", classLocal},
	0x9123683E: {"btrfs", classLocal},
	0x2FC12FC1: {"zfs", classLocal},
	0x4D44:     {"vfat", classLocal}, // persistent, but no POSIX semantics worth trusting
	0x01021994: {"tmpfs (RAM — data is lost on restart)", classEphemeral},
	0x794C7630: {"overlayfs (container layer — wiped on redeploy)", classEphemeral},
	0x6969:     {"NFS", classNetwork},
	0xFF534D42: {"CIFS/SMB — THE ADR-0003 FAILURE MODE", classNetwork},
	0xFE534D42: {"SMB2 — THE ADR-0003 FAILURE MODE", classNetwork},
	0x65735546: {"FUSE", classNetwork},
	0x73717368: {"squashfs (read-only)", classEphemeral},
	0x1021997:  {"9p/v9fs (WSL drvfs — passes locks only because a Windows server backs it)", classForeign},
}

// classifyFS reports what the probe directory actually sits on. On a managed
// host this is often decisive on its own, before a single byte is written.
func classifyFS(dir string) (label, class string, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return "", classUnknown, err
	}
	t := int64(st.Type)
	if k, ok := fsTypes[t]; ok {
		return fmt.Sprintf("0x%x  %s", t, k.name), k.class, nil
	}
	return fmt.Sprintf("0x%x  (unrecognised)", t), classUnknown, nil
}

// mountFor returns the /proc/mounts line whose mount point is the longest
// prefix of dir — i.e. the filesystem the probe directory actually sits on.
func mountFor(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", err
	}
	defer f.Close()

	best, bestLen := "", -1
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mp := fields[1]
		if abs == mp || strings.HasPrefix(abs, strings.TrimSuffix(mp, "/")+"/") {
			if len(mp) > bestLen {
				best, bestLen = line, len(mp)
			}
		}
	}
	if best == "" {
		return "", errors.New("no mount point matched")
	}
	return best, nil
}

// takeWriteLock is the exact primitive SQLite relies on: an advisory write lock
// on a byte range, taken non-blocking so a conflict reports rather than hangs.
func takeWriteLock(f *os.File) error {
	lk := &syscall.Flock_t{
		Type:   syscall.F_WRLCK,
		Whence: 0, // SEEK_SET
		Start:  0,
		Len:    1,
	}
	return syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, lk)
}

// isLockConflict distinguishes "another process holds it" — the correct answer —
// from "this filesystem cannot do locks at all".
func isLockConflict(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES)
}

func bootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}
