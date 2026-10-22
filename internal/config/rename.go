package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// oldName is what qissh was called before, and the name of the config
// and cache folders it made then.
const oldName = "quick-ssh"

// MoveOldFolders renames the quick-ssh config and cache folders to qissh,
// so the config, history, facts and backups carry over. A folder is left
// alone when there's already a qissh one beside it.
func MoveOldFolders() error {
	var errs []error
	for _, dir := range []func() (string, error){os.UserConfigDir, os.UserCacheDir} {
		parent, err := dir()
		if err != nil {
			continue // no such folder on this system: nothing to move
		}
		errs = append(errs, moveOldFolder(parent))
	}
	return errors.Join(errs...)
}

func moveOldFolder(parent string) error {
	old, cur := filepath.Join(parent, oldName), filepath.Join(parent, "qissh")
	if _, err := os.Stat(old); err != nil {
		return nil
	}
	if _, err := os.Stat(cur); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return os.Rename(old, cur)
}
