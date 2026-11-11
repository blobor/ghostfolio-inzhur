//go:build unix

package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func wantToken(t *testing.T, s *Store, startToken, want string, wantSource Source) {
	t.Helper()

	got, source, err := s.Token(startToken)
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if got != want || source != wantSource {
		t.Errorf("Token() = %q (%s), want %q (%s)", got, source, want, wantSource)
	}
}

func TestTokenWithoutStateIsStartToken(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir())

	wantToken(t, s, "start-1", "start-1", SourceStart)
}

func TestTokenAfterSaveIsStoredToken(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir())

	if err := s.Save("rotated-1", "start-1"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	wantToken(t, s, "start-1", "rotated-1", SourceStored)

	if err := s.Save("rotated-2", "start-1"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	wantToken(t, s, "start-1", "rotated-2", SourceStored)
}

func TestNewStartTokenReplacesStoredToken(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir())

	if err := s.Save("rotated-1", "start-1"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	wantToken(t, s, "start-2", "start-2", SourceStart)
}

func TestStateStaysBetweenRuns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	first, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := first.Save("rotated-1", "start-1"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second := open(t, dir)
	wantToken(t, second, "start-1", "rotated-1", SourceStored)
}

func TestOpenReturnsErrLockedForSecondRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := open(t, dir)

	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open() error = %v, want ErrLocked", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	open(t, dir)
}

func TestSaveRejectsEmptyToken(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir())

	if err := s.Save("", "start-1"); err == nil {
		t.Fatal("Save() error = nil, want an error")
	}
}

func TestStateFileIsPrivateAndHasNoStartToken(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "data")
	s := open(t, dir)

	if err := s.Save("rotated-1", "start-1"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != fileMode {
		t.Errorf("state file mode = %o, want %o", got, fileMode)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != dirMode {
		t.Errorf("data directory mode = %o, want %o", got, dirMode)
	}

	//nolint:gosec // G304: The directory is a temporary directory of the test.
	data, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "start-1") {
		t.Error("state file contains the start token")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("data directory has %d entries, want 2 (state file and lock)", len(entries))
	}
}

func TestTokenReportsDamagedStateFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := open(t, dir)

	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte("{not json"), fileMode); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Token("start-1"); err == nil {
		t.Fatal("Token() error = nil, want an error")
	}
}
