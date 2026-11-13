package hook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// gcRoots keeps indirect permanent roots next to the persistent queue. Both
// systemd's StateDirectory and the Darwin module already preserve this location.
// The indirect registration is owned by Nix; the queue never writes gcroots/auto.
type gcRoots struct {
	directory string
	command   func(context.Context, string, ...string) error
	stat      func(string) (os.FileInfo, error)
}

var outputPathPattern = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[a-zA-Z0-9+._?=-]+$`)

func validateOutputPath(path string) error {
	if !outputPathPattern.MatchString(path) || strings.HasSuffix(path, ".drv") {
		return fmt.Errorf("expected canonical store output path: %q", path)
	}

	return nil
}

// OpenQueueWithRoots opens the persistent upload queue and retains each pending
// output against Nix GC. Existing queue entries and roots left by an interrupted
// enqueue/removal are reconciled before the server can acknowledge new work.
func OpenQueueWithRoots(dbPath string) (*Queue, error) {
	return openQueueWithRoots(dbPath, nil, os.Lstat)
}

// Command execution and store existence checks are injectable so tests need no
// running Nix daemon and never register roots in the machine's real store.
func openQueueWithRoots(dbPath string, command func(context.Context, string, ...string) error, stat func(string) (os.FileInfo, error)) (*Queue, error) {
	absolute, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolving queue path: %w", err)
	}
	q, err := OpenQueue(absolute)
	if err != nil {
		return nil, err
	}
	if command == nil {
		command = func(ctx context.Context, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			// Keep exclusive queue ownership until Nix finishes registering a
			// root, even if the hook process exits while that child is running.
			cmd.ExtraFiles = []*os.File{q.owner}
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("registering Nix GC root: %w", err)
			}

			return nil
		}
	}
	q.roots = &gcRoots{directory: filepath.Join(filepath.Dir(q.path), "gcroots"), command: command, stat: stat}
	if err := q.restoreRoots(); err != nil {
		_ = q.Close()

		return nil, err
	}

	return q, nil
}

func (r *gcRoots) path(output string) string {
	return filepath.Join(r.directory, filepath.Base(output))
}

func (r *gcRoots) register(output string) error {
	if err := validateOutputPath(output); err != nil {
		return err
	}
	if _, err := r.stat(output); err != nil {
		return fmt.Errorf("checking pending output: %w", err)
	}
	if target, err := os.Readlink(r.path(output)); err == nil && target != output {
		return fmt.Errorf("unexpected pending root target: %q", target)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking pending root: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Realisation is only used to register an already valid output atomically
	// with Nix's GC. Disable local builds, remote builders, and substitution so
	// recovery/enqueueing can never build or fetch a missing store path.
	return r.command(ctx, "nix-store", "--option", "max-jobs", "0", "--option", "builders", "",
		"--option", "substitute", "false", "--add-root", r.path(output), "--indirect", "--realise", output)
}

func (r *gcRoots) remove(output string) error {
	root := r.path(output)
	target, err := os.Readlink(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking completed root: %w", err)
	}
	if target != output {
		return fmt.Errorf("unexpected completed root target: %q", target)
	}
	if err := os.Remove(root); err != nil {
		return fmt.Errorf("removing completed root: %w", err)
	}

	return nil
}

func (q *Queue) restoreRoots() error {
	r := q.roots
	if err := os.MkdirAll(r.directory, 0o700); err != nil {
		return fmt.Errorf("creating pending roots directory: %w", err)
	}
	info, err := os.Lstat(r.directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("pending roots location must be a directory: %q", r.directory)
	}
	if err := os.Chmod(r.directory, 0o700); err != nil {
		return fmt.Errorf("protecting pending roots directory: %w", err)
	}
	entries, err := os.ReadDir(r.directory)
	if err != nil {
		return fmt.Errorf("reading pending roots: %w", err)
	}
	count, err := q.Count()
	if err != nil {
		return err
	}
	paths, err := q.FetchBatch(count)
	if err != nil {
		return err
	}
	// Roots can predate their queue transaction or outlive a committed removal
	// after a crash. Requeue them rather than silently discard retained output.
	pending := make(map[string]struct{}, len(paths)+len(entries))
	for _, p := range paths {
		pending[p] = struct{}{}
	}
	for _, entry := range entries {
		p := "/nix/store/" + entry.Name()
		if err := validateOutputPath(p); err != nil {
			return err
		}
		target, err := os.Readlink(r.path(p))
		if err != nil || target != p {
			return fmt.Errorf("unexpected persisted GC root: %q", r.path(p))
		}
		pending[p] = struct{}{}
	}
	for p := range pending {
		if err := validateOutputPath(p); err != nil {
			return err
		}
		if _, err := r.stat(p); errors.Is(err, os.ErrNotExist) {
			// Explicitly handle outputs lost before the queue was upgraded, or
			// invalidated outside Nix. A permission/transient error is not loss.
			if err := q.remove([]string{p}); err != nil {
				return err
			}
			if err := r.remove(p); err != nil {
				return err
			}

			continue
		} else if err != nil {
			return fmt.Errorf("checking persisted output: %w", err)
		}
		if err := r.register(p); err != nil {
			return err
		}
		if err := q.enqueue([]string{p}); err != nil {
			return err
		}
	}

	return nil
}
