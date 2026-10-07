package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"syscall"

	"github.com/enderu/seeurchin/internal/config"
)

// dropPrivileges switches the process to PUID/PGID (the linuxserver.io
// convention) so files written to a mounted volume get the host user's
// ownership. It only acts when started as root: the database directory and
// files are chowned first, then supplementary groups, GID and UID are dropped.
// Unset PUID/PGID is a no-op, keeping the previous run-as-is behavior.
func dropPrivileges(cfg config.Config) error {
	if cfg.PUID < 0 {
		return nil
	}
	if os.Getuid() != 0 {
		if os.Getuid() != cfg.PUID || os.Getgid() != cfg.PGID {
			log.Printf("warning: PUID/PGID=%d/%d ignored; not running as root (uid=%d gid=%d)",
				cfg.PUID, cfg.PGID, os.Getuid(), os.Getgid())
		}
		return nil
	}

	if err := chownDB(cfg.DBPath, cfg.PUID, cfg.PGID); err != nil {
		return err
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(cfg.PGID); err != nil {
		return fmt.Errorf("setgid %d: %w", cfg.PGID, err)
	}
	if err := syscall.Setuid(cfg.PUID); err != nil {
		return fmt.Errorf("setuid %d: %w", cfg.PUID, err)
	}
	log.Printf("running as uid=%d gid=%d", cfg.PUID, cfg.PGID)
	return nil
}

// chownDB hands the SQLite file, its WAL/SHM siblings and their directory to
// uid:gid so the server can still create and write them after dropping root.
// The directory is skipped when it is the filesystem root.
func chownDB(dbPath string, uid, gid int) error {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return err
	}
	paths := []string{abs, abs + "-wal", abs + "-shm"}
	if dir := filepath.Dir(abs); dir != "/" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		paths = append(paths, dir)
	}
	for _, p := range paths {
		if err := os.Lchown(p, uid, gid); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("chown %s: %w", p, err)
		}
	}
	return nil
}
