//go:build !linux

package main

import (
	"log"

	"github.com/enderu/seeurchin/internal/config"
)

// dropPrivileges is only supported on Linux (the container target).
func dropPrivileges(cfg config.Config) error {
	if cfg.PUID >= 0 {
		log.Printf("warning: PUID/PGID are only supported on Linux; ignoring")
	}
	return nil
}
