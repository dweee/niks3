package hook //nolint:testpackage // Exercises injected GC registration and queue crash recovery internals.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// rootFixture emulates Nix registration and store metadata. Every filesystem
// write is beneath t.TempDir; no real Nix GC roots or store paths are touched.
type rootFixture struct {
	mu       sync.Mutex
	db       string
	info     os.FileInfo
	calls    [][]string
	statErrs map[string]error
	failPath string
	before   func(string)
}

func newRootFixture(t *testing.T) *rootFixture {
	t.Helper()
	directory := t.TempDir()
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}

	return &rootFixture{db: filepath.Join(directory, "upload-queue.db"), info: info, statErrs: make(map[string]error)}
}

func fixtureOutput(name string) string {
	return "/nix/store/" + strings.Repeat("0", 32) + "-" + name
}

func (f *rootFixture) root(output string) string {
	return filepath.Join(filepath.Dir(f.db), "gcroots", filepath.Base(output))
}

func (f *rootFixture) stat(path string) (os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.statErrs[path]; err != nil {
		return nil, err
	}

	return f.info, nil
}

func (f *rootFixture) command(_ context.Context, name string, args ...string) error {
	output := args[len(args)-1]
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	fail := f.failPath == output
	before := f.before
	f.mu.Unlock()
	if before != nil {
		before(output)
	}
	if fail {
		return errors.New("synthetic root registration failure")
	}
	root := f.root(output)
	if existing, err := os.Readlink(root); err == nil && existing == output {
		return nil
	}

	if err := os.Symlink(output, root); err != nil {
		return fmt.Errorf("creating fixture root: %w", err)
	}

	return nil
}

func (f *rootFixture) open(t *testing.T) *Queue {
	t.Helper()
	q, err := openQueueWithRoots(f.db, f.command, f.stat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })

	return q
}

func requireRoot(t *testing.T, f *rootFixture, output string, want bool) {
	t.Helper()
	target, err := os.Readlink(f.root(output))
	if want && (err != nil || target != output) {
		t.Fatalf("expected retained root for %s: target=%q err=%v", output, target, err)
	}
	if !want && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no root for %s: target=%q err=%v", output, target, err)
	}
}

func requireQueueCount(t *testing.T, q *Queue, want int) {
	t.Helper()
	count, err := q.Count()
	if err != nil || count != want {
		t.Fatalf("queue count=%d, want %d, err=%v", count, want, err)
	}
}

func TestRootedQueueAcknowledgesOnlyAfterRootRegistrationAndPersistence(t *testing.T) {
	t.Parallel()
	f := newRootFixture(t)
	q := f.open(t)
	output := fixtureOutput("built-output")
	entered, release := make(chan struct{}), make(chan struct{})
	f.before = func(string) { close(entered); <-release }
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	srv := NewServer(nil, q.Enqueue)
	go srv.handleConn(server)
	if err := json.NewEncoder(client).Encode(Request{Paths: []string{output}}); err != nil {
		t.Fatal(err)
	}
	<-entered
	ack := make(chan Response, 1)
	go func() {
		var response Response
		_ = json.NewDecoder(client).Decode(&response)
		ack <- response
	}()
	requireQueueCount(t, q, 0)
	select {
	case response := <-ack:
		t.Fatalf("acknowledged before registration: %+v", response)
	default:
	}
	close(release)
	if response := <-ack; response.Status != "ok" {
		t.Fatalf("enqueue response: %+v", response)
	}
	requireRoot(t, f, output, true)
	requireQueueCount(t, q, 1)
	wantCommand := []string{"nix-store", "--option", "max-jobs", "0", "--option", "builders", "", "--option", "substitute", "false", "--add-root", f.root(output), "--indirect", "--realise", output}
	if !reflect.DeepEqual(f.calls[0], wantCommand) {
		t.Fatalf("unsafe or unexpected root command: %#v", f.calls[0])
	}
}

func TestRootsSurviveRetryAndQueueRestart(t *testing.T) {
	t.Parallel()
	f := newRootFixture(t)
	q := f.open(t)
	output := fixtureOutput("pending")
	if err := q.Enqueue([]string{output}); err != nil {
		t.Fatal(err)
	}
	if err := q.Retry([]string{output}); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	requireRoot(t, f, output, true)
	reopened := f.open(t)
	requireQueueCount(t, reopened, 1)
	requireRoot(t, f, output, true)
}

func TestFailedUploadAndCancelledUploadKeepRoots(t *testing.T) {
	t.Parallel()
	for _, cancelled := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelled), func(t *testing.T) {
			t.Parallel()
			f := newRootFixture(t)
			q := f.open(t)
			output := fixtureOutput("unpublished")
			if err := q.Enqueue([]string{output}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			w := NewWorker(q, func(context.Context, []string) ([]string, error) {
				return nil, errors.New("synthetic upload failure")
			}, 1, nil)
			if w.upload(ctx, []string{output}) {
				t.Fatal("failed upload reported progress")
			}
			requireQueueCount(t, q, 1)
			requireRoot(t, f, output, true)
		})
	}
}

func TestSuccessfulClosurePublicationReleasesQueuedDependencyRoots(t *testing.T) {
	t.Parallel()
	f := newRootFixture(t)
	q := f.open(t)
	dependency, parent := fixtureOutput("dependency"), fixtureOutput("parent")
	if err := q.Enqueue([]string{dependency, parent}); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(q, func(context.Context, []string) ([]string, error) {
		return []string{dependency, parent}, nil
	}, 1, nil)
	if !w.upload(context.Background(), []string{parent}) {
		t.Fatal("confirmed upload made no progress")
	}
	requireQueueCount(t, q, 0)
	requireRoot(t, f, dependency, false)
	requireRoot(t, f, parent, false)
}

func TestPartialEnqueueRootFailureIsNotAcknowledgedAndRecoveredOnRestart(t *testing.T) {
	t.Parallel()
	f := newRootFixture(t)
	q := f.open(t)
	first, second := fixtureOutput("first"), fixtureOutput("second")
	f.failPath = second
	if err := q.Enqueue([]string{first, second}); err == nil {
		t.Fatal("partially registered batch was acknowledged")
	}
	requireQueueCount(t, q, 0)
	requireRoot(t, f, first, true)
	requireRoot(t, f, second, false)
	_ = q.Close()
	f.failPath = ""
	reopened := f.open(t)
	requireQueueCount(t, reopened, 1)
	paths, _ := reopened.FetchBatch(2)
	if !reflect.DeepEqual(paths, []string{first}) {
		t.Fatalf("partial enqueue recovery: %v", paths)
	}
}

func TestDatabaseEnqueueFailureKeepsRootForRestartRecovery(t *testing.T) {
	t.Parallel()
	f := newRootFixture(t)
	q := f.open(t)
	output := fixtureOutput("before-commit")
	_ = q.db.Close()
	if err := q.Enqueue([]string{output}); err == nil {
		t.Fatal("enqueue to closed database succeeded")
	}
	requireRoot(t, f, output, true)
	reopened := f.open(t)
	requireQueueCount(t, reopened, 1)
}

func TestFailedDatabaseRemovalDoesNotReleaseRoot(t *testing.T) {
	t.Parallel()
	f := newRootFixture(t)
	q := f.open(t)
	output := fixtureOutput("keep-on-delete-error")
	if err := q.Enqueue([]string{output}); err != nil {
		t.Fatal(err)
	}
	_ = q.db.Close()
	if err := q.Remove([]string{output}); err == nil {
		t.Fatal("removal from closed database succeeded")
	}
	requireRoot(t, f, output, true)
}

func TestOutputValidationRejectsEntireBatchBeforeRootCommands(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"relative", "/nix/store/short-name", fixtureOutput("foo") + "/child", fixtureOutput("foo.drv"), fixtureOutput("foo;touch-x"), fixtureOutput("foo") + "/../escape"} {
		t.Run(invalid, func(t *testing.T) {
			t.Parallel()
			f := newRootFixture(t)
			q := f.open(t)
			if err := q.Enqueue([]string{fixtureOutput("valid"), invalid}); err == nil {
				t.Fatalf("accepted invalid output %q", invalid)
			}
			if len(f.calls) != 0 {
				t.Fatalf("executed root commands before validating batch: %v", f.calls)
			}
			requireQueueCount(t, q, 0)
		})
	}
}

func TestConcurrentEnqueueAndRemoveNeverLeaveQueuedOutputUnrooted(t *testing.T) {
	t.Parallel()
	f := newRootFixture(t)
	q := f.open(t)
	output := fixtureOutput("concurrent")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := q.Enqueue([]string{output}); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if err := q.Remove([]string{output}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	count, err := q.Count()
	if err != nil {
		t.Fatal(err)
	}
	requireRoot(t, f, output, count == 1)
}

func TestStartupRootsPreexistingQueueAndExplicitlyDropsMissingOutput(t *testing.T) {
	t.Parallel()
	f := newRootFixture(t)
	q, err := OpenQueue(f.db)
	if err != nil {
		t.Fatal(err)
	}
	existing, missing := fixtureOutput("older-queue-entry"), fixtureOutput("already-lost")
	if err := q.Enqueue([]string{existing, missing}); err != nil {
		t.Fatal(err)
	}
	_ = q.Close()
	f.statErrs[missing] = os.ErrNotExist
	rooted := f.open(t)
	requireQueueCount(t, rooted, 1)
	requireRoot(t, f, existing, true)
	requireRoot(t, f, missing, false)
}

func TestWorkerDoesNotTreatInspectionFailureAsInvalidOutput(t *testing.T) {
	t.Parallel()
	f := newRootFixture(t)
	q := f.open(t)
	output := fixtureOutput("permission-denied")
	if err := q.Enqueue([]string{output}); err != nil {
		t.Fatal(err)
	}
	f.statErrs[output] = os.ErrPermission
	w := NewWorker(q, func(context.Context, []string) ([]string, error) {
		t.Fatal("uninspectable output should not be uploaded")

		return nil, nil
	}, 1, nil)
	if _, progress := w.step(context.Background()); progress {
		t.Fatal("inspection failure reported progress")
	}
	requireQueueCount(t, q, 1)
	requireRoot(t, f, output, true)
	f.statErrs[output] = os.ErrNotExist
	if _, progress := w.step(context.Background()); !progress {
		t.Fatal("explicitly missing output was not handled")
	}
	requireQueueCount(t, q, 0)
	requireRoot(t, f, output, false)
}

func TestStartupRejectsRootDirectorySymlinksAndUnexpectedTargets(t *testing.T) {
	t.Parallel()
	t.Run("directory symlink", func(t *testing.T) {
		t.Parallel()
		f := newRootFixture(t)
		if err := os.Symlink(t.TempDir(), filepath.Join(filepath.Dir(f.db), "gcroots")); err != nil {
			t.Fatal(err)
		}
		if q, err := openQueueWithRoots(f.db, f.command, f.stat); err == nil {
			_ = q.Close()
			t.Fatal("accepted redirected roots directory")
		}
	})
	t.Run("unexpected root target", func(t *testing.T) {
		t.Parallel()
		f := newRootFixture(t)
		q := f.open(t)
		_ = q.Close()
		if err := os.Symlink("/private/unrelated", f.root(fixtureOutput("bad-target"))); err != nil {
			t.Fatal(err)
		}
		if q, err := openQueueWithRoots(f.db, f.command, f.stat); err == nil {
			_ = q.Close()
			t.Fatal("accepted unrelated symlink target")
		}
	})
}

func TestWorkerUploadsDanglingSymlinkOutput(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	output := filepath.Join(directory, "symlink-output")
	if err := os.Symlink("/missing-target-is-valid-output-content", output); err != nil {
		t.Fatal(err)
	}
	q, err := OpenQueue(filepath.Join(directory, "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	if err := q.Enqueue([]string{output}); err != nil {
		t.Fatal(err)
	}
	uploaded := false
	w := NewWorker(q, func(_ context.Context, paths []string) ([]string, error) {
		uploaded = true

		return paths, nil
	}, 1, nil)
	if _, progress := w.step(context.Background()); !progress || !uploaded {
		t.Fatal("valid symlink output was discarded instead of published")
	}
	requireQueueCount(t, q, 0)
}
