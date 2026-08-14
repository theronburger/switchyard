package diskusage

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

type OSWalker struct{}

func (OSWalker) Lstat(ctx context.Context, path string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	return entryFromInfo(filepath.Base(path), info), nil
}

func (OSWalker) OpenDirectory(ctx context.Context, path string) (Directory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &osDirectory{file: file, path: path}, nil
}

type osDirectory struct {
	file *os.File
	path string
}

func (directory *osDirectory) Read(ctx context.Context, maximum int) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directoryEntries, readErr := directory.file.ReadDir(maximum)
	entries := make([]Entry, 0, len(directoryEntries))
	for _, directoryEntry := range directoryEntries {
		if err := ctx.Err(); err != nil {
			return entries, err
		}
		path := filepath.Join(directory.path, directoryEntry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			entries = append(entries, Entry{Name: directoryEntry.Name(), Err: err})
			continue
		}
		entries = append(entries, entryFromInfo(directoryEntry.Name(), info))
	}
	if errors.Is(readErr, io.EOF) {
		return entries, io.EOF
	}
	return entries, readErr
}

func (directory *osDirectory) Close() error {
	return directory.file.Close()
}

func entryFromInfo(name string, info fs.FileInfo) Entry {
	entry := Entry{
		Name:         name,
		Kind:         kindFromMode(info.Mode()),
		LogicalBytes: nonnegativeSize(info.Size()),
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return entry
	}
	entry.MetadataAvailable = true
	entry.Device = uint64(stat.Dev)
	entry.Inode = uint64(stat.Ino)
	entry.Links = uint64(stat.Nlink)
	if stat.Blocks > 0 {
		blocks := uint64(stat.Blocks)
		if blocks > ^uint64(0)/512 {
			entry.AllocatedBytes = ^uint64(0)
		} else {
			entry.AllocatedBytes = blocks * 512
		}
	}
	return entry
}

func kindFromMode(mode fs.FileMode) EntryKind {
	switch {
	case mode.IsRegular():
		return EntryRegular
	case mode.IsDir():
		return EntryDirectory
	case mode&fs.ModeSymlink != 0:
		return EntrySymlink
	default:
		return EntryOther
	}
}

func nonnegativeSize(size int64) uint64 {
	if size <= 0 {
		return 0
	}
	return uint64(size)
}
