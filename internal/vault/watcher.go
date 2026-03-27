// Package vault – watcher.go
//
// The Watcher polls Vault KV v2 metadata endpoints to detect secret version
// changes. When any watched path increments its version, it fires the provided
// callback so the controller can enqueue an immediate reconcile.
//
// Each VaultEtcdSync CR gets one Watcher; the controller creates and cancels
// watchers via a sync.Map keyed by NamespacedName.
package vault

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// ChangeCallback is called by the Watcher whenever any watched path changes.
// The implementation must be non-blocking (i.e. send on a buffered channel).
type ChangeCallback func()

// Watcher polls Vault KV v2 metadata for a set of paths and calls cb when any
// path's version changes. It stops when ctx is cancelled.
type Watcher struct {
	client   *Client
	paths    []string
	interval time.Duration
	cb       ChangeCallback
	log      logr.Logger

	// known tracks the last seen version per path.
	mu    sync.Mutex
	known map[string]int
}

// NewWatcher creates a Watcher for the given Vault data paths.
// pollInterval is the period between metadata checks.
func NewWatcher(
	client *Client,
	paths []string,
	pollInterval time.Duration,
	cb ChangeCallback,
	log logr.Logger,
) *Watcher {
	known := make(map[string]int, len(paths))
	for _, p := range paths {
		known[p] = -1 // -1 = not yet observed
	}
	return &Watcher{
		client:   client,
		paths:    paths,
		interval: pollInterval,
		cb:       cb,
		log:      log.WithName("vault-watcher"),
		known:    known,
	}
}

// Run starts the polling loop. It blocks until ctx is cancelled.
// Intended to run in a dedicated goroutine per CR.
func (w *Watcher) Run(ctx context.Context) {
	w.log.Info("vault watcher started", "paths", w.paths, "interval", w.interval)
	defer w.log.Info("vault watcher stopped")

	// Seed the known versions immediately on start to avoid a spurious
	// change notification on the very first tick.
	w.seedVersions(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.checkForChanges(ctx) {
				w.cb()
			}
		}
	}
}

// seedVersions does an initial metadata read on all paths and records the
// current versions as the baseline, suppressing a spurious "first tick" event.
func (w *Watcher) seedVersions(ctx context.Context) {
	for _, path := range w.paths {
		sv, err := w.client.GetSecretVersion(ctx, path)
		if err != nil {
			// Non-fatal on seed: we'll fire the callback on the next tick if
			// the version was truly zero/unknown.
			w.log.Error(err, "vault watcher: seed version check failed", "path", path)
			continue
		}
		w.mu.Lock()
		w.known[path] = sv.Version
		w.mu.Unlock()
		w.log.V(1).Info("vault watcher: seeded", "path", path, "version", sv.Version)
	}
}

// checkForChanges polls all paths. Returns true if any path's version changed.
func (w *Watcher) checkForChanges(ctx context.Context) bool {
	changed := false
	for _, path := range w.paths {
		sv, err := w.client.GetSecretVersion(ctx, path)
		if err != nil {
			w.log.Error(err, "vault watcher: metadata poll failed", "path", path)
			continue
		}

		w.mu.Lock()
		prev := w.known[path]
		if sv.Version != prev {
			w.log.Info("vault secret version changed",
				"path", path,
				"prevVersion", prev,
				"newVersion", sv.Version,
				"updatedAt", sv.UpdatedTime.Format(time.RFC3339),
			)
			w.known[path] = sv.Version
			changed = true
		}
		w.mu.Unlock()
	}
	return changed
}

// ─── WatcherRegistry ─────────────────────────────────────────────────────────

// WatcherRegistry manages per-CR watcher goroutines. It maps a NamespacedName
// string to a cancel function, allowing the controller to start/stop watchers.
type WatcherRegistry struct {
	cancels sync.Map // map[string]context.CancelFunc
}

// StartOrReplace starts a new watcher goroutine for key, stopping any existing
// one first. The key is typically types.NamespacedName.String().
func (r *WatcherRegistry) StartOrReplace(key string, w *Watcher, parentCtx context.Context) {
	// Stop existing watcher for this key if any.
	r.Stop(key)

	ctx, cancel := context.WithCancel(parentCtx)
	r.cancels.Store(key, cancel)

	go func() {
		defer r.cancels.Delete(key)
		w.Run(ctx)
	}()
}

// Stop cancels the watcher for key, if one exists.
func (r *WatcherRegistry) Stop(key string) {
	if v, ok := r.cancels.LoadAndDelete(key); ok {
		v.(context.CancelFunc)()
	}
}

// StopAll cancels every registered watcher.
func (r *WatcherRegistry) StopAll() {
	r.cancels.Range(func(k, v interface{}) bool {
		v.(context.CancelFunc)()
		r.cancels.Delete(k)
		return true
	})
}

// parseSyncInterval parses a duration string and returns a time.Duration,
// defaulting to 60 s on parse failure.
func ParseSyncInterval(s string) (time.Duration, error) {
	if s == "" {
		return 60 * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid syncInterval %q: %w", s, err)
	}
	if d < time.Second {
		return 0, fmt.Errorf("syncInterval %q is too short (minimum 1s)", s)
	}
	return d, nil
}
