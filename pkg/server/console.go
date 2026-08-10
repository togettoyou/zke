package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateConsoleDirectory(directory string) error {
	if directory == "" {
		return nil
	}
	info, err := os.Stat(directory)
	if err != nil {
		return fmt.Errorf("open Console directory %q: %w", directory, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Console directory %q is not a directory", directory)
	}
	indexPath := filepath.Join(directory, "index.html")
	indexInfo, err := os.Stat(indexPath)
	if err != nil {
		return fmt.Errorf("open Console entrypoint %q: %w", indexPath, err)
	}
	if !indexInfo.Mode().IsRegular() {
		return fmt.Errorf("Console entrypoint %q is not a regular file", indexPath)
	}
	if strings.TrimSpace(directory) != directory {
		return fmt.Errorf("Console directory must not contain surrounding whitespace")
	}
	return nil
}
