package diskusage

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type category uint8

const (
	categoryOther category = iota
	categoryNodeModules
)

type hardlinkRecord struct {
	category category
	entry    Entry
}

func NewScanner(config Config) (*Scanner, error) {
	limits := config.Limits
	if limits.MaximumEntries == 0 {
		limits.MaximumEntries = DefaultMaximumEntries
	}
	if limits.MaximumDuration == 0 {
		limits.MaximumDuration = DefaultMaximumDuration
	}
	if limits.MaximumDepth == 0 {
		limits.MaximumDepth = DefaultMaximumDepth
	}
	if limits.ReadBatchSize == 0 {
		limits.ReadBatchSize = DefaultReadBatchSize
	}
	if limits.MaximumEntries < 1 || limits.MaximumEntries > HardMaximumEntries ||
		limits.MaximumDuration <= 0 || limits.MaximumDuration > HardMaximumDuration ||
		limits.MaximumDepth < 1 || limits.MaximumDepth > HardMaximumDepth ||
		limits.ReadBatchSize < 1 || limits.ReadBatchSize > 4096 {
		return nil, ErrInvalidConfig
	}
	if config.Walker == nil {
		config.Walker = OSWalker{}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Scanner{
		walker: config.Walker, now: config.Now, limits: limits,
		allowCrossFilesystem: config.AllowCrossFilesystem,
	}, nil
}

func (scanner *Scanner) Scan(ctx context.Context, root string) (Report, error) {
	if strings.TrimSpace(root) == "" {
		return Report{}, ErrInvalidRoot
	}
	startedAt := scanner.now()
	state := &scanState{
		report:    Report{Reasons: make([]PartialReason, 0)},
		reasons:   make(map[PartialReason]struct{}),
		startedAt: startedAt,
	}
	hardlinks := make(map[inodeKey]hardlinkRecord)
	scanContext, cancel := context.WithTimeout(ctx, scanner.limits.MaximumDuration)
	defer cancel()

	if err := scanner.check(scanContext, ctx, state); err != nil || state.stop {
		scanner.finish(state)
		return state.report, err
	}
	rootEntry, err := scanner.walker.Lstat(scanContext, root)
	if err != nil {
		if contextErr := scanner.contextError(scanContext, ctx, state); contextErr != nil || state.stop {
			scanner.finish(state)
			return state.report, contextErr
		}
		return Report{}, ErrRootRead
	}
	if rootEntry.Kind != EntryDirectory || rootEntry.Err != nil || !rootEntry.MetadataAvailable {
		return Report{}, ErrInvalidRoot
	}
	state.rootDevice = rootEntry.Device
	state.report.EntriesVisited = 1
	scanner.addUsage(state, categoryOther, rootEntry)

	scanner.scanDirectory(scanContext, ctx, state, hardlinks, root, categoryOther, 0)
	scanner.finish(state)
	if err := ctx.Err(); err != nil {
		return state.report, err
	}
	return state.report, nil
}

func (scanner *Scanner) scanDirectory(
	scanContext context.Context,
	callerContext context.Context,
	state *scanState,
	hardlinks map[inodeKey]hardlinkRecord,
	path string,
	parentCategory category,
	depth int,
) {
	if state.stop {
		return
	}
	directory, err := scanner.walker.OpenDirectory(scanContext, path)
	if err != nil {
		if scanner.contextError(scanContext, callerContext, state) == nil && !state.stop {
			scanner.addReason(state, ReasonReadFailure)
		}
		return
	}
	defer func() {
		if err := directory.Close(); err != nil {
			scanner.addReason(state, ReasonReadFailure)
		}
	}()
	seenNames := make(map[string]struct{})

	for !state.stop {
		if err := scanner.check(scanContext, callerContext, state); err != nil || state.stop {
			return
		}
		remaining := scanner.limits.MaximumEntries - state.report.EntriesVisited
		if remaining == 0 {
			scanner.addReason(state, ReasonEntryLimit)
			state.stop = true
			return
		}
		batchSize := scanner.limits.ReadBatchSize
		if uint64(batchSize) > remaining {
			batchSize = int(remaining)
		}
		entries, readErr := directory.Read(scanContext, batchSize)
		if len(entries) > batchSize {
			scanner.addReason(state, ReasonInvalidEntry)
			state.stop = true
			return
		}
		for _, entry := range entries {
			if err := scanner.check(scanContext, callerContext, state); err != nil || state.stop {
				return
			}
			if state.report.EntriesVisited >= scanner.limits.MaximumEntries {
				scanner.addReason(state, ReasonEntryLimit)
				state.stop = true
				return
			}
			state.report.EntriesVisited++
			if entry.Err != nil {
				scanner.addReason(state, ReasonReadFailure)
				continue
			}
			if !validEntryName(entry.Name) || !validEntryKind(entry.Kind) {
				scanner.addReason(state, ReasonInvalidEntry)
				continue
			}
			if _, duplicate := seenNames[entry.Name]; duplicate {
				scanner.addReason(state, ReasonInvalidEntry)
				continue
			}
			seenNames[entry.Name] = struct{}{}
			entryCategory := childCategory(parentCategory, entry.Name)
			if !entry.MetadataAvailable {
				scanner.addReason(state, ReasonMetadataUnavailable)
				if entry.Kind != EntryDirectory {
					scanner.addUsage(state, entryCategory, entry)
				}
				continue
			}
			if !scanner.allowCrossFilesystem && entry.Device != state.rootDevice {
				scanner.addReason(state, ReasonCrossFilesystem)
				continue
			}
			if entry.Kind == EntryRegular && entry.Links > 1 {
				if entry.Inode == 0 {
					scanner.addReason(state, ReasonMetadataUnavailable)
					scanner.addUsage(state, entryCategory, entry)
					continue
				}
				key := inodeKey{device: entry.Device, inode: entry.Inode}
				if existing, duplicate := hardlinks[key]; duplicate {
					state.report.HardLinksDeduplicated++
					if entry.LogicalBytes != existing.entry.LogicalBytes || entry.AllocatedBytes != existing.entry.AllocatedBytes {
						scanner.addReason(state, ReasonInvalidEntry)
					}
					if entryCategory > existing.category {
						scanner.moveCategoryUsage(state, existing.category, entryCategory, existing.entry)
						existing.category = entryCategory
						hardlinks[key] = existing
					}
					continue
				}
				hardlinks[key] = hardlinkRecord{category: entryCategory, entry: entry}
			}
			scanner.addUsage(state, entryCategory, entry)
			if entry.Kind == EntryDirectory {
				if depth+1 > scanner.limits.MaximumDepth {
					scanner.addReason(state, ReasonDepthLimit)
					continue
				}
				childPath := filepath.Join(path, entry.Name)
				scanner.scanDirectory(scanContext, callerContext, state, hardlinks, childPath, entryCategory, depth+1)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return
			}
			if scanner.contextError(scanContext, callerContext, state) == nil && !state.stop {
				scanner.addReason(state, ReasonReadFailure)
			}
			return
		}
		if len(entries) == 0 {
			scanner.addReason(state, ReasonReadFailure)
			return
		}
	}
}

func (scanner *Scanner) check(scanContext, callerContext context.Context, state *scanState) error {
	if err := scanner.contextError(scanContext, callerContext, state); err != nil || state.stop {
		return err
	}
	elapsed := scanner.now().Sub(state.startedAt)
	if elapsed >= scanner.limits.MaximumDuration {
		scanner.addReason(state, ReasonTimeLimit)
		state.stop = true
	}
	return nil
}

func (scanner *Scanner) contextError(scanContext, callerContext context.Context, state *scanState) error {
	if err := callerContext.Err(); err != nil {
		scanner.addReason(state, ReasonCancelled)
		state.stop = true
		return err
	}
	if errors.Is(scanContext.Err(), context.DeadlineExceeded) {
		scanner.addReason(state, ReasonTimeLimit)
		state.stop = true
	}
	return nil
}

func (scanner *Scanner) finish(state *scanState) {
	duration := scanner.now().Sub(state.startedAt)
	if duration < 0 {
		duration = 0
	}
	state.report.Duration = duration
	order := []PartialReason{
		ReasonCancelled, ReasonEntryLimit, ReasonTimeLimit, ReasonDepthLimit,
		ReasonReadFailure, ReasonCrossFilesystem, ReasonMetadataUnavailable,
		ReasonNumericOverflow, ReasonInvalidEntry,
	}
	for _, reason := range order {
		if _, exists := state.reasons[reason]; exists {
			state.report.Reasons = append(state.report.Reasons, reason)
		}
	}
}

func (scanner *Scanner) addReason(state *scanState, reason PartialReason) {
	state.report.Partial = true
	state.reasons[reason] = struct{}{}
}

func (scanner *Scanner) addUsage(state *scanState, selected category, entry Entry) {
	addUsageValues(state, &state.report.Total, entry)
	addUsageValues(state, scanner.categoryUsage(&state.report.Categories, selected), entry)
}

func addUsageValues(state *scanState, usage *Usage, entry Entry) {
	usage.LogicalBytes = addSaturating(state, usage.LogicalBytes, entry.LogicalBytes)
	usage.AllocatedBytes = addSaturating(state, usage.AllocatedBytes, entry.AllocatedBytes)
	switch entry.Kind {
	case EntryRegular:
		usage.Files++
	case EntryDirectory:
		usage.Directories++
	case EntrySymlink:
		usage.Symlinks++
	default:
		usage.OtherEntries++
	}
}

func addSaturating(state *scanState, current, value uint64) uint64 {
	if ^uint64(0)-current < value {
		state.report.Partial = true
		state.reasons[ReasonNumericOverflow] = struct{}{}
		return ^uint64(0)
	}
	return current + value
}

func (scanner *Scanner) moveCategoryUsage(state *scanState, from, to category, entry Entry) {
	fromUsage := scanner.categoryUsage(&state.report.Categories, from)
	toUsage := scanner.categoryUsage(&state.report.Categories, to)
	fromUsage.LogicalBytes -= entry.LogicalBytes
	fromUsage.AllocatedBytes -= entry.AllocatedBytes
	switch entry.Kind {
	case EntryRegular:
		fromUsage.Files--
	case EntryDirectory:
		fromUsage.Directories--
	case EntrySymlink:
		fromUsage.Symlinks--
	default:
		fromUsage.OtherEntries--
	}
	addUsageValues(state, toUsage, entry)
}

func (*Scanner) categoryUsage(categories *Categories, selected category) *Usage {
	if selected == categoryNodeModules {
		return &categories.NodeModules
	}
	return &categories.Other
}

func childCategory(parent category, name string) category {
	if parent == categoryNodeModules || name == "node_modules" {
		return categoryNodeModules
	}
	return categoryOther
}

func validEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && !filepath.IsAbs(name) && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}

func validEntryKind(kind EntryKind) bool {
	return kind == EntryRegular || kind == EntryDirectory || kind == EntrySymlink || kind == EntryOther
}
