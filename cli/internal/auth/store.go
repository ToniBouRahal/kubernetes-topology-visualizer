package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	credentialsFile = "credentials.json"
	lockFile        = "credentials.lock"
	fileVersion     = 1

	// DefaultLockTimeout bounds how long a process waits for another one's read-modify-write
	// (D-15.11). A refresh holds the lock across one token request, which NewHTTPClient caps
	// below this.
	DefaultLockTimeout = 30 * time.Second
)

// errUnchanged, returned by an Update callback, releases the lock without rewriting the file.
var errUnchanged = errors.New("unchanged")

// Entry is one server's sign-in. Everything but the two tokens is configuration the user gave at
// login, saved so later commands need at most --server (D-15.9).
type Entry struct {
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
	CAFile   string   `json:"ca_file,omitempty"`

	IDToken      string    `json:"id_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
	RefreshToken string    `json:"refresh_token,omitempty"`
}

// SignedIn reports whether the entry still holds an ID token. A failed refresh clears the tokens
// but keeps the configuration, so `login --server` alone signs back in.
func (e *Entry) SignedIn() bool { return e != nil && e.IDToken != "" }

// caFile returns the CA saved with the sign-in. Login stores an absolute path; a relative one
// (hand-edited, say) is refused rather than resolved against whatever directory a later process
// happens to run in, where a file of that name would be trusted for the server and the token
// endpoint (D-15.14).
func (e *Entry) caFile() (string, error) {
	if e.CAFile != "" && !filepath.IsAbs(e.CAFile) {
		return "", fmt.Errorf("the saved --ca-file %q is not an absolute path; run login again with an absolute --ca-file", e.CAFile)
	}
	return e.CAFile, nil
}

func (e *Entry) clearTokens() {
	e.IDToken, e.RefreshToken, e.Expiry = "", "", time.Time{}
}

// File is the whole credentials file, keyed by NormalizeServer.
type File struct {
	Version int               `json:"version"`
	Servers map[string]*Entry `json:"servers"`
}

// Store is the credentials file and its lock in one directory.
type Store struct {
	Dir         string
	LockTimeout time.Duration
}

// DefaultStore is $(os.UserConfigDir)/kubectl-topology.
func DefaultStore() (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locating the config directory: %w", err)
	}
	return &Store{Dir: filepath.Join(base, "kubectl-topology"), LockTimeout: DefaultLockTimeout}, nil
}

func (s *Store) path() string { return filepath.Join(s.Dir, credentialsFile) }

// Load reads the file without locking: writers replace it by rename, so a reader sees either the
// old or the new file, never half of one. A missing file is an empty one.
func (s *Store) Load() (*File, error) {
	if err := s.checkDir(false); err != nil {
		return nil, err
	}
	return s.read()
}

// Update runs fn on the current file under the exclusive lock and writes the result back before
// releasing it. fn returning an error leaves the file as it was.
func (s *Store) Update(ctx context.Context, fn func(*File) error) error {
	if err := s.checkDir(true); err != nil {
		return err
	}
	timeout := s.LockTimeout
	if timeout <= 0 {
		timeout = DefaultLockTimeout
	}
	unlock, err := lockExclusive(ctx, filepath.Join(s.Dir, lockFile), timeout)
	if err != nil {
		return err
	}
	defer unlock()

	f, err := s.read()
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		if errors.Is(err, errUnchanged) {
			return nil
		}
		return err
	}
	return s.write(f)
}

// checkDir refuses a directory other users can reach into (as ssh does with ~/.ssh), creating it
// 0700 first when create is set.
func (s *Store) checkDir(create bool) error {
	info, err := os.Stat(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		if !create {
			return nil
		}
		if err := os.MkdirAll(s.Dir, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", s.Dir, err)
		}
		// MkdirAll applies the umask; set the mode explicitly so a lax umask cannot widen it.
		if err := os.Chmod(s.Dir, 0o700); err != nil {
			return fmt.Errorf("securing %s: %w", s.Dir, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", s.Dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", s.Dir)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("credentials directory %s is accessible by other users (mode %04o); fix with: chmod 700 %s",
			s.Dir, perm, s.Dir)
	}
	return nil
}

func (s *Store) read() (*File, error) {
	p := s.path()
	empty := &File{Version: fileVersion, Servers: map[string]*Entry{}}
	info, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("credentials file %s is accessible by other users (mode %04o); fix with: chmod 600 %s",
			p, perm, p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	f := empty
	// The decode error is reported without its detail: json's messages can quote file content.
	if err := json.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("%s is not a valid credentials file; delete it and sign in again", p)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("%s has version %d, this binary reads %d", p, f.Version, fileVersion)
	}
	if f.Servers == nil {
		f.Servers = map[string]*Entry{}
	}
	return f, nil
}

// write replaces the file atomically: a 0600 temporary file in the same directory, synced, then
// renamed over the old one.
func (s *Store) write(f *File) error {
	f.Version = fileVersion
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}
	tmp, err := os.CreateTemp(s.Dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	// CreateTemp already uses 0600; Chmod states it rather than relying on that.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing credentials: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := os.Rename(tmpName, s.path()); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	tmpName = ""
	if dir, err := os.Open(s.Dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
