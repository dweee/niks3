package hook //nolint:testpackage // Exercises the production registration closure and queue owner descriptor.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestQueueOwnerRejectsCanonicalAndSymlinkAliases(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(realDir, "queue.db")
	q, err := OpenQueue(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })

	fileAlias := filepath.Join(dir, "queue-alias.db")
	if err := os.Symlink(db, fileAlias); err != nil {
		t.Fatal(err)
	}
	paths := []string{db, filepath.Join(alias, "queue.db"), fileAlias}
	for _, path := range paths {
		second, err := OpenQueue(path)
		if second != nil {
			_ = second.Close()
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Fatalf("second owner for %q: got %v, want owner-lock contention", path, err)
		}
	}

	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		reopened, err := OpenQueue(path)
		if err != nil {
			t.Fatalf("reopen %q after owner close: %v", path, err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

//nolint:paralleltest,nolintlint // The isolated subprocess fixture requires t.Setenv.
func TestQueueOwnerRegistrationChildKeepsLock(t *testing.T) {
	if os.Getenv("NIKS3_QUEUE_OWNER_TEST_HELPER") == "1" { //nolint:usetesting // Child process intentionally reads its parent's fixture environment.
		queueOwnerRegistrationChild(t)

		return
	}

	dir := t.TempDir()
	db := filepath.Join(dir, "queue.db")
	q, err := OpenQueueWithRoots(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	ownerFD := int(q.owner.Fd())
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = null.Close() })

	ready := filepath.Join(dir, "ready")
	release := filepath.Join(dir, "release")
	for _, path := range []string{ready, release} {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	readyPipe := openOwnerPipe(t, ready)
	releasePipe := openOwnerPipe(t, release)

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	// The fake registrar re-executes only this test's helper branch. It never
	// invokes Nix, creates roots, or reads any real store paths.
	script := "#!" + shell + "\nexec \"$NIKS3_QUEUE_OWNER_TEST_BINARY\" -test.run '^TestQueueOwnerRegistrationChildKeepsLock$'\n"
	if err := os.WriteFile(filepath.Join(bin, "nix-store"), []byte(script), 0o700); err != nil { //nolint:gosec // Private temporary registrar fixture must be executable.
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("NIKS3_QUEUE_OWNER_TEST_BINARY", executable)
	t.Setenv("NIKS3_QUEUE_OWNER_TEST_HELPER", "1")
	t.Setenv("NIKS3_QUEUE_OWNER_TEST_LOCK", q.path+".lock")
	t.Setenv("NIKS3_QUEUE_OWNER_TEST_READY", ready)
	t.Setenv("NIKS3_QUEUE_OWNER_TEST_RELEASE", release)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	var commandErr error
	go func() {
		// This is the real default registration closure installed by
		// OpenQueueWithRoots, including its ExtraFiles owner descriptor.
		commandErr = q.roots.command(ctx, "nix-store")
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	started := make(chan error, 1)
	go func() {
		var signal [1]byte
		_, err := io.ReadFull(readyPipe, signal[:])
		if err == nil && signal[0] != 'R' {
			err = fmt.Errorf("unexpected registrar signal %q", signal[0])
		}
		started <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-done:
		t.Fatalf("registrar exited before proving inherited ownership: %v", commandErr)
	case <-ctx.Done():
		t.Fatal("registrar did not establish its inherited owner descriptor")
	}

	// Atomically close the parent's lock descriptor and reserve its number with
	// /dev/null. This leaves the Go File object untouched while os/exec uses it,
	// and prevents cleanup from closing an unrelated, reused descriptor. The
	// child must now be the sole holder of the queue lock's open description.
	if err := syscall.Dup2(int(null.Fd()), ownerFD); err != nil {
		t.Fatal(err)
	}
	second, err := OpenQueue(db)
	if second != nil {
		_ = second.Close()
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("parent descriptor closed while registrar lives: got %v, want owner-lock contention", err)
	}

	if _, err := releasePipe.Write([]byte{'X'}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if commandErr != nil {
			t.Fatalf("registrar helper: %v", commandErr)
		}
	case <-ctx.Done():
		t.Fatal("registrar did not exit after release handshake")
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenQueue(db)
	if err != nil {
		t.Fatalf("owner was not released after registrar exit: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func openOwnerPipe(t *testing.T, path string) *os.File {
	t.Helper()
	pipe, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipe.Close() })

	return pipe
}

func queueOwnerRegistrationChild(t *testing.T) {
	t.Helper()
	inherited := os.NewFile(3, "inherited-queue-owner")
	if inherited == nil {
		t.Fatal("missing inherited owner descriptor")
	}
	defer func() { _ = inherited.Close() }()
	actual, err := inherited.Stat()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.Stat(os.Getenv("NIKS3_QUEUE_OWNER_TEST_LOCK")) //nolint:gosec // Only this test parent supplies these private temporary fixture paths.
	if err != nil || !os.SameFile(actual, expected) {
		t.Fatalf("descriptor3 is not the queue owner lock: %v", err)
	}
	if err := syscall.Flock(int(inherited.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("inherited descriptor does not own the lock: %v", err)
	}
	ready, err := os.OpenFile(os.Getenv("NIKS3_QUEUE_OWNER_TEST_READY"), os.O_WRONLY, 0) //nolint:gosec // Only this test parent supplies these private temporary fixture paths.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ready.Close() }()
	release, err := os.OpenFile(os.Getenv("NIKS3_QUEUE_OWNER_TEST_RELEASE"), os.O_RDONLY, 0) //nolint:gosec // Only this test parent supplies these private temporary fixture paths.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release.Close() }()
	if _, err := ready.Write([]byte{'R'}); err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := io.ReadFull(release, signal[:]); err != nil || signal[0] != 'X' {
		t.Fatalf("release handshake: signal=%q err=%v", signal[0], err)
	}
}
