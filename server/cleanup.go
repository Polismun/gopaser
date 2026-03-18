package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// cleanupTempFiles removes stale tmp-* files left by previous crashes.
// Called once at startup before binding HTTP handlers.
func cleanupTempFiles() {
	dirs := []string{
		demosDir,
		filepath.Join(mediaDir, "images"),
		filepath.Join(mediaDir, "videos"),
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "tmp-") {
				path := filepath.Join(dir, e.Name())
				if err := os.Remove(path); err == nil {
					log.Printf("Cleaned up stale temp file: %s", path)
				}
			}
		}
	}
}
