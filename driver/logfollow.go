package steamcmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// logPollInterval is how often followed files are checked. Polling (rather
// than inotify) keeps this working on NFS/CSI volumes, where change
// notifications are unreliable.
const logPollInterval = 500 * time.Millisecond

// resolveFollowPaths turns follow_logs entries into absolute paths; relative
// entries resolve against installDir (the launched process's working dir).
func resolveFollowPaths(installDir string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(installDir, p)
		}
		out = append(out, p)
	}
	return out
}

// snapshotFiles records which of paths already exist. It MUST be called
// before the launched process starts: a file that exists at that moment is
// left over from a previous run, whereas one that appears afterwards belongs
// to this run. Taking the snapshot inside the follower goroutine would race
// with a server that creates its log within milliseconds of starting.
func snapshotFiles(paths []string) map[string]os.FileInfo {
	snap := make(map[string]os.FileInfo, len(paths))
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			snap[p] = st
		}
	}
	return snap
}

// lockedWriter serialises writes from several follower goroutines so whole
// lines are never interleaved.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// followLogs copies appended lines from each path to w until ctx is
// cancelled, then does a final drain and returns. Many game servers log only
// to a file and print nothing to stdout, so without this `nomad alloc logs`
// stays empty.
//
// stale is the pre-launch snapshot (see snapshotFiles). A file identical to
// its snapshot (same inode, size and mtime) is ignored. If the same inode has
// since grown, following resumes at the old size (append-style logs); if it
// shrank it was truncated, so following restarts at 0. A replaced file
// (new inode, e.g. rotated by the game) is followed from the start.
func followLogs(ctx context.Context, paths []string, stale map[string]os.FileInfo, w io.Writer, poll time.Duration) {
	lw := &lockedWriter{w: w}
	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			followFile(ctx, p, stale[p], lw, poll)
		}(p)
	}
	wg.Wait()
}

func followFile(ctx context.Context, path string, stale os.FileInfo, out io.Writer, poll time.Duration) {
	var (
		f       *os.File
		partial []byte
		buf     = make([]byte, 64*1024)
	)
	defer func() {
		if f != nil {
			f.Close()
		}
		if len(partial) > 0 {
			out.Write(append(partial, '\n'))
		}
	}()

	// drain reads everything currently available from f.
	drain := func() {
		for {
			n, err := f.Read(buf)
			if n > 0 {
				partial = append(partial, buf[:n]...)
				for {
					i := bytes.IndexByte(partial, '\n')
					if i < 0 {
						break
					}
					out.Write(partial[:i+1])
					partial = partial[i+1:]
				}
			}
			if err != nil || n == 0 {
				return
			}
		}
	}

	step := func() {
		pst, perr := os.Stat(path)
		if f == nil {
			if perr != nil {
				return
			}
			var offset int64
			if stale != nil && os.SameFile(stale, pst) {
				if pst.Size() == stale.Size() && pst.ModTime().Equal(stale.ModTime()) {
					return // untouched: still the previous run's file
				}
				if pst.Size() >= stale.Size() {
					offset = stale.Size() // appended to: skip what was already there
				}
			}
			nf, err := os.Open(path)
			if err != nil {
				return
			}
			if offset > 0 {
				if _, err := nf.Seek(offset, io.SeekStart); err != nil {
					nf.Close()
					return
				}
			}
			f = nf
			stale = nil
		}
		fst, ferr := f.Stat()
		if ferr == nil {
			if pos, err := f.Seek(0, io.SeekCurrent); err == nil && fst.Size() < pos {
				// Truncated in place. Detected by "file is now shorter than our
				// read position"; a rewrite that is already as long again by the
				// next poll cannot be told from an append and is missed (games
				// that do this usually rotate to a new inode instead).
				f.Seek(0, io.SeekStart)
				partial = nil
			}
		}
		drain()
		// Replaced (rotated): finish the old file, then pick up the new one.
		if perr != nil || (ferr == nil && !os.SameFile(fst, pst)) {
			f.Close()
			f = nil
		}
	}

	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		step()
		select {
		case <-ctx.Done():
			step() // final drain
			return
		case <-t.C:
		}
	}
}
