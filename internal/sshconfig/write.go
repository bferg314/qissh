package sshconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrChanged means the file changed on disk since it was read, so writing
// would throw those changes away.
var ErrChanged = errors.New("the file changed on disk since it was read")

// Validator checks that ssh accepts the config file at path, typically by
// running `ssh -F path -G <alias>`.
type Validator func(path string) error

// keepBackups is how many backups of each file are kept.
const keepBackups = 10

// Save writes f over the file it was read from, safely:
//
//  1. the file on disk must still be what f was parsed from (ErrChanged);
//  2. the new content goes to a temp file beside it, which validate must
//     accept, so ssh never sees a config it can't read;
//  3. the old content is copied to backupDir (the last 10 are kept) and to
//     <file>.qs-bak beside it;
//  4. the temp file is renamed over the original (it was made in the same
//     folder, so it gets the folder's permissions, which OpenSSH on
//     Windows checks), and validate runs once more on the real path; if
//     that fails the old content is put back.
//
// A symlinked file is written where the link points, and the link is
// left as it is.
//
// It returns the path of the backup in backupDir ("" for a new file).
func Save(f *File, validate Validator, backupDir string) (backup string, err error) {
	old, err := os.ReadFile(f.Path)
	exists := err == nil
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", err
	case !bytes.Equal(old, f.Orig):
		return "", ErrChanged
	}

	// When the config is a symlink (into a dotfiles repo, say), write the
	// file it points to, so the link stays a link.
	target := f.Path
	if exists {
		if target, err = filepath.EvalSymlinks(f.Path); err != nil {
			return "", err
		}
	}

	dir := filepath.Dir(target)
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".qs-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // a no-op once renamed
	_, werr := tmp.Write(f.Bytes())
	if err := errors.Join(werr, tmp.Close(), os.Chmod(tmpPath, mode)); err != nil {
		return "", err
	}
	if validate != nil {
		if err := validate(tmpPath); err != nil {
			return "", fmt.Errorf("ssh rejected the change, nothing was written: %w", err)
		}
	}

	if exists {
		if backup, err = writeBackup(backupDir, f.Path, old); err != nil {
			return "", fmt.Errorf("backing up %s: %w", f.Path, err)
		}
		_ = os.WriteFile(f.Path+".qs-bak", old, mode)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return backup, err
	}
	if validate != nil {
		if err := validate(f.Path); err != nil {
			if exists {
				_ = os.WriteFile(f.Path, old, mode)
			} else {
				_ = os.Remove(f.Path)
			}
			return backup, fmt.Errorf("ssh rejected the file once in place, so it was put back: %w", err)
		}
	}
	return backup, nil
}

// writeBackup saves data as <dir>/<name>-<time> and prunes old backups.
func writeBackup(dir, path string, data []byte) (string, error) {
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	prefix := filepath.Base(path) + "-"
	stamp := prefix + time.Now().Format("20060102-150405.000")
	var name string
	for n := 0; ; n++ {
		name = filepath.Join(dir, stamp)
		if n > 0 {
			name += fmt.Sprintf("-%d", n) // two saves in the same millisecond
		}
		out, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := out.Write(data)
		if err := errors.Join(werr, out.Close()); err != nil {
			return "", err
		}
		break
	}
	entries, _ := os.ReadDir(dir)
	var mine []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			mine = append(mine, e.Name())
		}
	}
	sort.Strings(mine) // the timestamp sorts oldest first
	for len(mine) > keepBackups {
		os.Remove(filepath.Join(dir, mine[0]))
		mine = mine[1:]
	}
	return name, nil
}
