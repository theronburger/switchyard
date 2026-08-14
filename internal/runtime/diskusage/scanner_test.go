package diskusage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeNode struct {
	entry    Entry
	children []Entry
	readErr  error
	openErr  error
	closeErr error
}

type fakeWalker struct {
	mutex sync.Mutex
	root  string
	nodes map[string]*fakeNode
	opens []string
}

func (walker *fakeWalker) Lstat(ctx context.Context, path string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	node, exists := walker.nodes[path]
	if !exists {
		return Entry{}, errors.New("private missing path")
	}
	return node.entry, node.entry.Err
}

func (walker *fakeWalker) OpenDirectory(ctx context.Context, path string) (Directory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	walker.mutex.Lock()
	defer walker.mutex.Unlock()
	node, exists := walker.nodes[path]
	if !exists {
		return nil, errors.New("private missing directory")
	}
	walker.opens = append(walker.opens, path)
	if node.openErr != nil {
		return nil, node.openErr
	}
	return &fakeDirectory{entries: append([]Entry(nil), node.children...), readErr: node.readErr, closeErr: node.closeErr}, nil
}

func (walker *fakeWalker) opened(path string) bool {
	walker.mutex.Lock()
	defer walker.mutex.Unlock()
	for _, opened := range walker.opens {
		if opened == path {
			return true
		}
	}
	return false
}

type fakeDirectory struct {
	entries  []Entry
	index    int
	readErr  error
	closeErr error
}

func (directory *fakeDirectory) Read(ctx context.Context, maximum int) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if directory.index == len(directory.entries) {
		if directory.readErr != nil {
			err := directory.readErr
			directory.readErr = nil
			return nil, err
		}
		return nil, io.EOF
	}
	end := directory.index + maximum
	if end > len(directory.entries) {
		end = len(directory.entries)
	}
	entries := append([]Entry(nil), directory.entries[directory.index:end]...)
	directory.index = end
	if directory.index == len(directory.entries) && directory.readErr == nil {
		return entries, io.EOF
	}
	return entries, nil
}

func (directory *fakeDirectory) Close() error {
	return directory.closeErr
}

func TestScannerAggregatesCategoriesAndDeduplicatesHardLinks(t *testing.T) {
	t.Parallel()
	root := "/worktree"
	walker := newFakeWalker(root)
	walker.addDirectory(root, directory("organizer"), directory("app"), file("other.txt", 400, 4, 1))
	walker.addDirectory(filepath.Join(root, "organizer"), file("code.js", 300, 3, 1), hardlink("shared-first", 50, 9))
	walker.addDirectory(filepath.Join(root, "app"), file("main.js", 100, 1, 1), directory("node_modules"), hardlink("shared-second", 50, 9))
	walker.addDirectory(filepath.Join(root, "app", "node_modules"), file("package.js", 200, 2, 1))

	report, err := scanWith(t, walker, Limits{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Partial {
		t.Fatalf("unexpected partial report: %+v", report.Reasons)
	}
	if report.Total.LogicalBytes != 1050 || report.Total.AllocatedBytes != 5*512 || report.Total.Files != 5 {
		t.Fatalf("unexpected totals: %+v", report.Total)
	}
	if report.Categories.NodeModules.LogicalBytes != 200 || report.Categories.NodeModules.Files != 1 {
		t.Fatalf("node_modules usage: %+v", report.Categories.NodeModules)
	}
	if report.Categories.App.LogicalBytes != 150 || report.Categories.App.Files != 2 {
		t.Fatalf("app usage: %+v", report.Categories.App)
	}
	if report.Categories.Organizer.LogicalBytes != 300 || report.Categories.Organizer.Files != 1 {
		t.Fatalf("organizer usage: %+v", report.Categories.Organizer)
	}
	if report.Categories.Other.LogicalBytes != 400 || report.Categories.Other.Files != 1 {
		t.Fatalf("other usage: %+v", report.Categories.Other)
	}
	if report.HardLinksDeduplicated != 1 || report.EntriesVisited != 10 {
		t.Fatalf("dedup/entry accounting: %+v", report)
	}
}

func TestScannerNeverFollowsSymlinksOrCrossesFilesystem(t *testing.T) {
	t.Parallel()
	root := "/worktree"
	walker := newFakeWalker(root)
	mount := directory("mounted")
	mount.Device = 2
	walker.addDirectory(root, mount, symlink("outside-link", 12))
	walker.addDirectory(filepath.Join(root, "mounted"), file("private-filename", 10_000, 44, 1))

	report, err := scanWith(t, walker, Limits{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Partial || !hasReason(report, ReasonCrossFilesystem) {
		t.Fatalf("cross-filesystem skip was not explicit: %+v", report)
	}
	if walker.opened(filepath.Join(root, "mounted")) {
		t.Fatal("scanner opened a directory on another filesystem")
	}
	if report.Total.LogicalBytes != 12 || report.Total.Symlinks != 1 || report.Total.Files != 0 {
		t.Fatalf("excluded entries affected totals: %+v", report.Total)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"worktree", "mounted", "outside-link", "private-filename"} {
		if strings.Contains(string(encoded), filename) {
			t.Fatalf("report leaked filename %q: %s", filename, encoded)
		}
	}
}

func TestScannerEntryAndDepthCapsReturnPartialReports(t *testing.T) {
	t.Parallel()
	t.Run("entries", func(t *testing.T) {
		root := "/worktree"
		walker := newFakeWalker(root)
		walker.addDirectory(root, file("one", 1, 1, 1), file("two", 1, 2, 1), file("three", 1, 3, 1))
		report, err := scanWith(t, walker, Limits{MaximumEntries: 3}).Scan(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		if !report.Partial || !hasReason(report, ReasonEntryLimit) || report.EntriesVisited != 3 || report.Total.Files != 2 {
			t.Fatalf("entry cap report: %+v", report)
		}
	})

	t.Run("depth", func(t *testing.T) {
		root := "/worktree"
		walker := newFakeWalker(root)
		walker.addDirectory(root, directory("one"))
		walker.addDirectory(filepath.Join(root, "one"), directory("two"))
		walker.addDirectory(filepath.Join(root, "one", "two"), file("hidden", 100, 1, 1))
		report, err := scanWith(t, walker, Limits{MaximumDepth: 1}).Scan(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		if !report.Partial || !hasReason(report, ReasonDepthLimit) || report.Total.Files != 0 {
			t.Fatalf("depth cap report: %+v", report)
		}
		if walker.opened(filepath.Join(root, "one", "two")) {
			t.Fatal("scanner traversed beyond maximum depth")
		}
	})
}

func TestScannerTimeCapUsesInjectedClock(t *testing.T) {
	t.Parallel()
	root := "/worktree"
	walker := newFakeWalker(root)
	walker.addDirectory(root, file("late", 100, 1, 1))
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	times := []time.Time{base, base, base.Add(2 * time.Second), base.Add(2 * time.Second)}
	var mutex sync.Mutex
	now := func() time.Time {
		mutex.Lock()
		defer mutex.Unlock()
		if len(times) == 1 {
			return times[0]
		}
		value := times[0]
		times = times[1:]
		return value
	}
	scanner, err := NewScanner(Config{Walker: walker, Now: now, Limits: Limits{MaximumDuration: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := scanner.Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Partial || !hasReason(report, ReasonTimeLimit) || report.Total.Files != 0 || report.Duration != 2*time.Second {
		t.Fatalf("time cap report: %+v", report)
	}
}

type blockingWalker struct {
	started chan struct{}
}

func (walker *blockingWalker) Lstat(context.Context, string) (Entry, error) {
	return rootDirectory(), nil
}

func (walker *blockingWalker) OpenDirectory(context.Context, string) (Directory, error) {
	return &blockingDirectory{started: walker.started}, nil
}

type blockingDirectory struct {
	started chan struct{}
	once    sync.Once
}

func (directory *blockingDirectory) Read(ctx context.Context, _ int) ([]Entry, error) {
	directory.once.Do(func() { close(directory.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*blockingDirectory) Close() error { return nil }

func TestScannerCancellationStopsTraversal(t *testing.T) {
	t.Parallel()
	walker := &blockingWalker{started: make(chan struct{})}
	scanner := scanWith(t, walker, Limits{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var report Report
	var scanErr error
	go func() {
		report, scanErr = scanner.Scan(ctx, "/worktree")
		close(done)
	}()
	<-walker.started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled scanner did not stop")
	}
	if !errors.Is(scanErr, context.Canceled) || !report.Partial || !hasReason(report, ReasonCancelled) {
		t.Fatalf("cancellation result: report=%+v err=%v", report, scanErr)
	}
}

func TestScannerReadFailuresAndInvalidEntriesAreRedacted(t *testing.T) {
	t.Parallel()
	root := "/worktree"
	walker := newFakeWalker(root)
	walker.addDirectory(root, Entry{Name: "../private-name", Kind: EntryRegular, MetadataAvailable: true, Device: 1}, Entry{Name: "gone", Err: errors.New("secret filename and path")})
	report, err := scanWith(t, walker, Limits{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasReason(report, ReasonInvalidEntry) || !hasReason(report, ReasonReadFailure) {
		t.Fatalf("partial reasons: %+v", report.Reasons)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "gone") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("partial report leaked details: %s", encoded)
	}

	rootFailure := &fakeWalker{root: root, nodes: map[string]*fakeNode{}}
	_, err = scanWith(t, rootFailure, Limits{}).Scan(context.Background(), root)
	if !errors.Is(err, ErrRootRead) || strings.Contains(err.Error(), root) {
		t.Fatalf("root error was not stable and redacted: %v", err)
	}
}

type oversizedBatchWalker struct{}

func (oversizedBatchWalker) Lstat(context.Context, string) (Entry, error) {
	return rootDirectory(), nil
}

func (oversizedBatchWalker) OpenDirectory(context.Context, string) (Directory, error) {
	return &oversizedBatchDirectory{}, nil
}

type oversizedBatchDirectory struct{}

func (*oversizedBatchDirectory) Read(context.Context, int) ([]Entry, error) {
	return []Entry{file("one", 1, 1, 1), file("two", 1, 2, 1)}, nil
}

func (*oversizedBatchDirectory) Close() error { return nil }

func TestScannerRejectsHostileOversizedWalkerBatch(t *testing.T) {
	t.Parallel()
	report, err := scanWith(t, oversizedBatchWalker{}, Limits{ReadBatchSize: 1}).Scan(context.Background(), "/worktree")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Partial || !hasReason(report, ReasonInvalidEntry) || report.Total.Files != 0 || report.EntriesVisited != 1 {
		t.Fatalf("hostile walker batch affected report: %+v", report)
	}
}

func TestScannerSaturatesNumericOverflow(t *testing.T) {
	t.Parallel()
	root := "/worktree"
	walker := newFakeWalker(root)
	walker.nodes[root].entry.LogicalBytes = ^uint64(0)
	walker.addDirectory(root, file("overflow", 1, 1, 1))
	report, err := scanWith(t, walker, Limits{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total.LogicalBytes != ^uint64(0) || !report.Partial || !hasReason(report, ReasonNumericOverflow) {
		t.Fatalf("numeric overflow was not saturated: %+v", report)
	}
}

func TestOSWalkerDoesNotFollowExternalSymlinkAndDeduplicatesHardLink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	external := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "app", "original.js")
	if err := os.WriteFile(original, []byte("12345678"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(root, "app", "hardlink.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "external-secret"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "external-link")); err != nil {
		t.Fatal(err)
	}

	scanner, err := NewScanner(Config{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := scanner.Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.HardLinksDeduplicated != 1 || report.Categories.App.Files != 1 || report.Total.Symlinks != 1 {
		t.Fatalf("OS scan did not enforce identities: %+v", report)
	}
	appInfo, err := os.Lstat(filepath.Join(root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Categories.App.LogicalBytes != nonnegativeSize(appInfo.Size())+8 {
		t.Fatalf("hard-linked file bytes were counted more than once: %+v", report.Categories.App)
	}
}

func newFakeWalker(root string) *fakeWalker {
	return &fakeWalker{root: root, nodes: map[string]*fakeNode{
		root: {entry: rootDirectory()},
	}}
}

func (walker *fakeWalker) addDirectory(path string, entries ...Entry) {
	node, exists := walker.nodes[path]
	if !exists {
		node = &fakeNode{entry: directory(filepath.Base(path))}
		walker.nodes[path] = node
	}
	node.children = append([]Entry(nil), entries...)
	for _, entry := range entries {
		if entry.Kind == EntryDirectory {
			childPath := filepath.Join(path, entry.Name)
			if _, exists := walker.nodes[childPath]; !exists {
				walker.nodes[childPath] = &fakeNode{entry: entry}
			}
		}
	}
}

func rootDirectory() Entry {
	return Entry{Name: "worktree", Kind: EntryDirectory, MetadataAvailable: true, Device: 1, Inode: 100, Links: 1}
}

func directory(name string) Entry {
	return Entry{Name: name, Kind: EntryDirectory, MetadataAvailable: true, Device: 1, Inode: inodeForName(name), Links: 1}
}

func file(name string, logical, inode, links uint64) Entry {
	return Entry{Name: name, Kind: EntryRegular, LogicalBytes: logical, AllocatedBytes: 512, MetadataAvailable: true, Device: 1, Inode: inode, Links: links}
}

func hardlink(name string, logical, inode uint64) Entry {
	return file(name, logical, inode, 2)
}

func symlink(name string, logical uint64) Entry {
	return Entry{Name: name, Kind: EntrySymlink, LogicalBytes: logical, AllocatedBytes: 0, MetadataAvailable: true, Device: 1, Inode: inodeForName(name), Links: 1}
}

func inodeForName(name string) uint64 {
	var value uint64 = 1000
	for _, character := range name {
		value = value*31 + uint64(character)
	}
	return value
}

func scanWith(t *testing.T, walker Walker, limits Limits) *Scanner {
	t.Helper()
	scanner, err := NewScanner(Config{Walker: walker, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	return scanner
}

func hasReason(report Report, expected PartialReason) bool {
	for _, reason := range report.Reasons {
		if reason == expected {
			return true
		}
	}
	return false
}
