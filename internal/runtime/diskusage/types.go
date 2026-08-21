package diskusage

import (
	"context"
	"errors"
	"time"
)

const (
	DefaultMaximumEntries  = 2_000_000
	HardMaximumEntries     = 10_000_000
	DefaultMaximumDuration = 2 * time.Minute
	HardMaximumDuration    = 30 * time.Minute
	DefaultMaximumDepth    = 128
	HardMaximumDepth       = 1024
	DefaultReadBatchSize   = 256
)

var (
	ErrInvalidConfig = errors.New("invalid disk usage scanner configuration")
	ErrInvalidRoot   = errors.New("invalid disk usage scan root")
	ErrRootRead      = errors.New("disk usage scan root is unavailable")
)

type EntryKind string

const (
	EntryRegular   EntryKind = "file"
	EntryDirectory EntryKind = "directory"
	EntrySymlink   EntryKind = "symlink"
	EntryOther     EntryKind = "other"
)

type Entry struct {
	Name              string
	Kind              EntryKind
	LogicalBytes      uint64
	AllocatedBytes    uint64
	Device            uint64
	Inode             uint64
	Links             uint64
	MetadataAvailable bool
	Err               error
}

type Directory interface {
	Read(ctx context.Context, maximum int) ([]Entry, error)
	Close() error
}

type Walker interface {
	Lstat(ctx context.Context, path string) (Entry, error)
	OpenDirectory(ctx context.Context, path string) (Directory, error)
}

type Limits struct {
	MaximumEntries  uint64
	MaximumDuration time.Duration
	MaximumDepth    int
	ReadBatchSize   int
}

type Config struct {
	Walker               Walker
	Now                  func() time.Time
	Limits               Limits
	AllowCrossFilesystem bool
}

type PartialReason string

const (
	ReasonCancelled           PartialReason = "cancelled"
	ReasonEntryLimit          PartialReason = "entry_limit"
	ReasonTimeLimit           PartialReason = "time_limit"
	ReasonDepthLimit          PartialReason = "depth_limit"
	ReasonReadFailure         PartialReason = "read_failure"
	ReasonCrossFilesystem     PartialReason = "cross_filesystem"
	ReasonMetadataUnavailable PartialReason = "metadata_unavailable"
	ReasonNumericOverflow     PartialReason = "numeric_overflow"
	ReasonInvalidEntry        PartialReason = "invalid_entry"
)

type Usage struct {
	LogicalBytes   uint64 `json:"logicalBytes"`
	AllocatedBytes uint64 `json:"allocatedBytes"`
	Files          uint64 `json:"files"`
	Directories    uint64 `json:"directories"`
	Symlinks       uint64 `json:"symlinks"`
	OtherEntries   uint64 `json:"otherEntries"`
}

type Categories struct {
	NodeModules Usage `json:"nodeModules"`
	Other       Usage `json:"other"`
}

type Report struct {
	Total                 Usage           `json:"total"`
	Categories            Categories      `json:"categories"`
	EntriesVisited        uint64          `json:"entriesVisited"`
	HardLinksDeduplicated uint64          `json:"hardLinksDeduplicated"`
	Partial               bool            `json:"partial"`
	Reasons               []PartialReason `json:"reasons"`
	Duration              time.Duration   `json:"duration"`
}

type Scanner struct {
	walker               Walker
	now                  func() time.Time
	limits               Limits
	allowCrossFilesystem bool
}

type inodeKey struct {
	device uint64
	inode  uint64
}

type scanState struct {
	report     Report
	rootDevice uint64
	reasons    map[PartialReason]struct{}
	startedAt  time.Time
	stop       bool
}
