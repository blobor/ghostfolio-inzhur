//go:build unix

package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	stateFileName = "state.json"
	lockFileName  = "lock"

	dirMode  = 0o700
	fileMode = 0o600
)

// ErrLocked reports that a different run uses the data directory.
var ErrLocked = errors.New("a different run uses the data directory")

// Source tells where a refresh token comes from.
type Source string

const (
	// SourceStart is the start token from the configuration.
	SourceStart Source = "start"
	// SourceStored is the token that a previous run saved.
	SourceStored Source = "stored"
)

// Store keeps the refresh token in a data directory. A Store holds a lock
// on the directory until Close.
type Store struct {
	dir  string
	lock *os.File
	now  func() time.Time
}

// file is the content of the state file.
type file struct {
	// RefreshToken is the token for the next refresh.
	RefreshToken string `json:"refresh_token"`
	// StartTokenSHA256 identifies the start token that started this session.
	StartTokenSHA256 string `json:"start_token_sha256"`
	// UpdatedAt is the time of the last save.
	UpdatedAt time.Time `json:"updated_at"`
}

// Open makes the data directory if it is missing and locks it. It returns
// ErrLocked if a different run holds the lock.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("make data directory: %w", err)
	}

	//nolint:gosec // G304: The directory comes from the configuration.
	lock, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock data directory: %w", err)
	}

	return &Store{dir: dir, lock: lock, now: time.Now}, nil
}

// Close releases the lock.
func (s *Store) Close() error {
	return s.lock.Close()
}

// Token returns the refresh token for the next refresh.
//
// It returns the start token if no token is stored, or if the start token
// is different from the one that started the stored session. If not, it
// returns the stored token.
func (s *Store) Token(startToken string) (string, Source, error) {
	f, err := s.read()
	if errors.Is(err, os.ErrNotExist) {
		return startToken, SourceStart, nil
	}
	if err != nil {
		return "", "", err
	}

	if f.RefreshToken == "" || f.StartTokenSHA256 != hash(startToken) {
		return startToken, SourceStart, nil
	}
	return f.RefreshToken, SourceStored, nil
}

// Save stores the refresh token that the last refresh returned. The start
// token is the one from the configuration of this run.
//
// Save replaces the state file in one step. Thus the file always has a
// complete token, also when the program stops during the write.
func (s *Store) Save(refreshToken, startToken string) error {
	if refreshToken == "" {
		return errors.New("refresh token is empty")
	}

	//nolint:gosec // G117: The purpose of the state file is to keep the token.
	data, err := json.MarshalIndent(file{
		RefreshToken:     refreshToken,
		StartTokenSHA256: hash(startToken),
		UpdatedAt:        s.now().UTC(),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	return s.writeAtomic(append(data, '\n'))
}

func (s *Store) path() string {
	return filepath.Join(s.dir, stateFileName)
}

func (s *Store) read() (file, error) {
	data, err := os.ReadFile(s.path())
	if err != nil {
		return file{}, fmt.Errorf("read state file: %w", err)
	}

	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return file{}, fmt.Errorf("decode state file %s: %w", s.path(), err)
	}
	return f, nil
}

// writeAtomic writes the data to a temporary file in the same directory,
// then renames it to the state file.
func (s *Store) writeAtomic(data []byte) (err error) {
	tmp, err := os.CreateTemp(s.dir, stateFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("make temporary state file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	if err = tmp.Chmod(fileMode); err != nil {
		return fmt.Errorf("set mode of state file: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync state file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	if err = os.Rename(tmp.Name(), s.path()); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}

	return syncDir(s.dir)
}

// syncDir makes the rename durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: The directory comes from the configuration.
	if err != nil {
		return fmt.Errorf("open data directory: %w", err)
	}
	defer func() { _ = d.Close() }()

	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync data directory: %w", err)
	}
	return nil
}

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
