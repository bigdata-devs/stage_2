package datalake

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const filePermissions = 0o644

// writeFileAtomically ports Stage 1 write_text_atomically: the content goes to a hidden
// temporary sibling (".<name>.*.tmp"), is flushed to disk and then renamed over the target.
// Readers therefore see either the previous file or the complete new one, never a truncated
// file, even if the process crashes mid-write. The temporary file is always closed before the
// rename because Windows cannot rename an open file.
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

// writeAndClose writes and syncs the content, and always closes the file.
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

// removeLeftover deletes the temporary file of a failed write; a failure here only leaves
// a hidden ".tmp" file that no reader ever matches, so it is deliberately ignored.
func removeLeftover(path string) {
	_ = os.Remove(path)
}
