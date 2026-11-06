package datalake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const filePermissions = 0o644

func writeFileAtomically(path string, content string) error {
	temporaryFile, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	if err := writeAndClose(temporaryFile, content); err != nil {
		removeLeftover(temporaryFile.Name())
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(temporaryFile.Name(), path); err != nil {
		removeLeftover(temporaryFile.Name())
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func writeAndClose(file *os.File, content string) error {
	writeErr := writeAndSync(file, content)
	return errors.Join(writeErr, file.Close())
}

func writeAndSync(file *os.File, content string) error {
	if err := file.Chmod(filePermissions); err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	return file.Sync()
}

func removeLeftover(path string) {
	_ = os.Remove(path)
}
