package configuration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// Desired-file editing. The daemon is the only writer of the private desired
// configuration; these helpers edit one generic repository entry inside the
// owner's YAML while preserving every other byte of meaning (comments, order,
// and untouched sections) and fail closed on anything that is not a plain,
// owner-only, singly linked regular file inside a private directory.

var (
	// ErrDesiredMissing reports that no desired file exists yet.
	ErrDesiredMissing = errors.New("desired configuration file does not exist")
	// ErrDesiredChanged reports a compare-and-swap failure on the desired file.
	ErrDesiredChanged = errors.New("desired configuration changed before this mutation")
	// ErrRepositoryMissing reports a mutation naming a key the file does not contain.
	ErrRepositoryMissing = errors.New("repository is not configured")
	// ErrRepositoryRootBound reports an attempt to repoint an existing key.
	ErrRepositoryRootBound = errors.New("repository key is bound to its root; remove and add instead")
)

// RepositoryEntry is the generic identity subset of one repository profile.
type RepositoryEntry struct {
	Key                  string
	Enabled              bool
	DisplayName          string
	Root                 string
	Remote               string
	DefaultBase          string
	ManagedWorktreesRoot string
}

// Desired is a bounded view of the current desired file.
type Desired struct {
	Present      bool
	Contents     []byte
	SourceDigest string
	Document     Document
	Problem      error
}

// Entries lists the generic repository entries sorted by key. It is empty when
// the file is absent or malformed.
func (desired Desired) Entries() []RepositoryEntry {
	if !desired.Present || desired.Problem != nil {
		return nil
	}
	entries := make([]RepositoryEntry, 0, len(desired.Document.Repositories))
	for _, key := range sortedRepositoryKeys(desired.Document.Repositories) {
		repository := desired.Document.Repositories[key]
		entries = append(entries, RepositoryEntry{
			Key: key, Enabled: repository.Enabled, DisplayName: repository.DisplayName, Root: repository.Root,
			Remote: repository.Git.Remote, DefaultBase: repository.Git.DefaultBase,
			ManagedWorktreesRoot: repository.Git.ManagedWorktreesRoot,
		})
	}
	return entries
}

// ReadDesired reads and inspects the desired file without fingerprinting
// executables. A missing file is reported with Present=false; a file that
// fails the private-file or schema rules is reported with a Problem so the
// owner can see why the daemon refuses it. The file itself is never modified.
func ReadDesired(path string) Desired {
	contents, err := readPrivateRegularFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Desired{}
		}
		return Desired{Present: true, Problem: err}
	}
	desired := Desired{Present: true, Contents: contents, SourceDigest: digest(contents)}
	document, err := Inspect(contents)
	if err != nil {
		desired.Problem = err
		return desired
	}
	desired.Document = document
	return desired
}

// UpsertRepository returns the desired file with entry added or, when key
// exists, with only its generic identity fields updated in place. Every other
// section of an existing entry is preserved byte-for-byte in meaning. An
// existing key cannot change root: that binding is permanent by design.
func UpsertRepository(contents []byte, entry RepositoryEntry) ([]byte, error) {
	root, repositories, err := parseEditableDocument(contents)
	if err != nil {
		return nil, err
	}
	if existing := mappingValue(repositories, entry.Key); existing != nil {
		if existing.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("repository %q is not a mapping", entry.Key)
		}
		if current := mappingValue(existing, "root"); current != nil && current.Value != entry.Root {
			return nil, ErrRepositoryRootBound
		}
		setScalar(existing, "enabled", boolNode(entry.Enabled))
		setScalar(existing, "displayName", stringNode(entry.DisplayName))
		setScalar(existing, "root", stringNode(entry.Root))
		git := mappingValue(existing, "git")
		if git == nil || git.Kind != yaml.MappingNode {
			git = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setScalar(existing, "git", git)
		}
		setScalar(git, "remote", stringNode(entry.Remote))
		setScalar(git, "defaultBase", stringNode(entry.DefaultBase))
		setScalar(git, "managedWorktreesRoot", stringNode(entry.ManagedWorktreesRoot))
	} else {
		repositories.Content = append(repositories.Content, stringNode(entry.Key), newRepositoryNode(entry))
	}
	return encodeDocument(root)
}

// RemoveRepository returns the desired file without the named entry.
func RemoveRepository(contents []byte, key string) ([]byte, error) {
	root, repositories, err := parseEditableDocument(contents)
	if err != nil {
		return nil, err
	}
	for index := 0; index+1 < len(repositories.Content); index += 2 {
		if repositories.Content[index].Value == key {
			repositories.Content = append(repositories.Content[:index:index], repositories.Content[index+2:]...)
			return encodeDocument(root)
		}
	}
	return nil, ErrRepositoryMissing
}

// NewDocument renders a fresh desired file containing one repository entry and
// the documented machine defaults. It is used only when no desired file exists.
func NewDocument(entry RepositoryEntry) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	top := root.Content[0]
	top.Content = append(top.Content,
		stringNode("schemaVersion"), intNode(SchemaVersion),
		stringNode("machine"), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			stringNode("ports"), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
				stringNode("first"), intNode(30000), stringNode("last"), intNode(49999),
			}},
			stringNode("execution"), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
				stringNode("inheritedEnvironment"), {Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle},
				stringNode("shellDefault"), stringNode("deny"),
			}},
		}},
		stringNode("secretProviders"), emptyMapNode(),
		stringNode("repositories"), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			stringNode(entry.Key), newRepositoryNode(entry),
		}},
	)
	return encodeDocument(root)
}

func parseEditableDocument(contents []byte) (*yaml.Node, *yaml.Node, error) {
	if len(contents) == 0 || len(contents) > maximumConfigurationBytes || bytes.IndexByte(contents, 0) >= 0 {
		return nil, nil, errors.New("desired configuration is empty, oversized, or binary")
	}
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(&root); err != nil {
		return nil, nil, fmt.Errorf("decode YAML: %w", err)
	}
	if err := validateYAMLNode(&root, 0); err != nil {
		return nil, nil, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("configuration must contain exactly one YAML document")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("configuration must be one YAML mapping")
	}
	top := root.Content[0]
	repositories := mappingValue(top, "repositories")
	if repositories == nil {
		repositories = emptyMapNode()
		top.Content = append(top.Content, stringNode("repositories"), repositories)
	}
	if repositories.Kind != yaml.MappingNode {
		if repositories.Kind == yaml.ScalarNode && repositories.Tag == "!!null" {
			*repositories = *emptyMapNode()
		} else {
			return nil, nil, errors.New("repositories must be a mapping")
		}
	}
	return &root, repositories, nil
}

func encodeDocument(root *yaml.Node) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("finish configuration: %w", err)
	}
	if buffer.Len() > maximumConfigurationBytes {
		return nil, fmt.Errorf("configuration exceeds %d bytes", maximumConfigurationBytes)
	}
	return buffer.Bytes(), nil
}

func newRepositoryNode(entry RepositoryEntry) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	node.Content = append(node.Content,
		stringNode("enabled"), boolNode(entry.Enabled),
		stringNode("displayName"), stringNode(entry.DisplayName),
		stringNode("root"), stringNode(entry.Root),
		stringNode("git"), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			stringNode("remote"), stringNode(entry.Remote),
			stringNode("defaultBase"), stringNode(entry.DefaultBase),
			stringNode("managedWorktreesRoot"), stringNode(entry.ManagedWorktreesRoot),
		}},
	)
	for _, section := range []string{"values", "toolchains", "caches", "environmentSources", "preparation", "targets"} {
		node.Content = append(node.Content, stringNode(section), emptyMapNode())
	}
	node.Content = append(node.Content, stringNode("defaultTarget"), stringNode(""))
	for _, section := range []string{"services", "infrastructure", "artifacts", "actions", "cleanup"} {
		node.Content = append(node.Content, stringNode(section), emptyMapNode())
	}
	return node
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Kind == yaml.ScalarNode && mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func setScalar(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Kind == yaml.ScalarNode && mapping.Content[index].Value == key {
			value.HeadComment = mapping.Content[index+1].HeadComment
			value.LineComment = mapping.Content[index+1].LineComment
			value.FootComment = mapping.Content[index+1].FootComment
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, stringNode(key), value)
}

func stringNode(value string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	// Quote anything YAML could retype or that looks like a path/ref with
	// special characters, so round-tripping never changes meaning.
	if value == "" || strings.ContainsAny(value, ":#{}[],&*!|>'\"%@`\n\t") || strings.TrimSpace(value) != value ||
		strings.EqualFold(value, "true") || strings.EqualFold(value, "false") || strings.EqualFold(value, "null") ||
		strings.EqualFold(value, "yes") || strings.EqualFold(value, "no") || strings.EqualFold(value, "~") ||
		looksNumeric(value) {
		node.Style = yaml.DoubleQuotedStyle
	}
	return node
}

func looksNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789.+-_eExXoO", character) {
			return false
		}
	}
	return true
}

func boolNode(value bool) *yaml.Node {
	text := "false"
	if value {
		text = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text}
}

func intNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(value)}
}

func emptyMapNode() *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
}

// Test hooks that interpose a concurrent editor inside the compare-and-swap
// window. beforeDesiredCommit runs once the temporary file is durable, before
// the destination is re-verified; beforeDesiredExchange runs after that
// re-verification and immediately before the kernel exchange, so the
// post-exchange proof and undo path can be driven deterministically.
var (
	beforeDesiredCommit   func(path string)
	beforeDesiredExchange func(path string)
)

// WriteDesired atomically replaces the desired file with contents under a
// compare-and-swap on the existing file. The parent directory must already be
// (or is created as) an owner-only private directory that is not a symlink;
// an existing destination must be a plain owner-only singly linked regular
// file whose bytes still digest to expectedSourceDigest (empty means the file
// must not exist). The write goes through an exclusive 0600 temporary file,
// fsync, an atomic exchange, and directory fsync.
//
// The destination stays open from validation to commit so the commit can prove
// it still names the same unchanged inode. A new file is linked with
// RENAME_EXCL, so a file that appears concurrently is never overwritten. An
// existing file is replaced with RENAME_SWAP, which moves the prior version to
// the temporary path instead of destroying it; the prior version is then
// re-verified through the held descriptor and, if a concurrent editor changed
// or replaced it inside the window, the exchange is undone so the editor's
// version stays at the destination. If that undo cannot be performed, the
// editor's version is left on disk at the temporary path and named in the
// error. Either way ErrDesiredChanged or an explicit error is returned and no
// version of the file is lost.
func WriteDesired(path string, contents []byte, expectedSourceDigest string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean != path {
		return errors.New("configuration path must be clean and absolute")
	}
	if len(contents) == 0 || len(contents) > maximumConfigurationBytes {
		return errors.New("desired configuration is empty or oversized")
	}
	directory := filepath.Dir(clean)
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	var current *privateRegularFile
	if expectedSourceDigest == "" {
		if _, err := os.Lstat(clean); err == nil {
			return ErrDesiredChanged
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect desired configuration: %w", err)
		}
	} else {
		opened, err := openPrivateRegularFile(clean)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return ErrDesiredChanged
			}
			return err
		}
		defer func() { _ = opened.Close() }()
		if err := opened.unchangedSince(expectedSourceDigest, opened.metadata); err != nil {
			return err
		}
		current = opened
	}

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("name temporary configuration: %w", err)
	}
	temporary := filepath.Join(directory, ".configuration."+hex.EncodeToString(suffix[:])+".tmp")
	descriptor, err := unix.Open(temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), temporary)
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = file.Close()
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict temporary configuration: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary configuration: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary configuration: %w", err)
	}
	if beforeDesiredCommit != nil {
		beforeDesiredCommit(clean)
	}

	if current == nil {
		err := unix.RenameatxNp(unix.AT_FDCWD, temporary, unix.AT_FDCWD, clean, unix.RENAME_EXCL)
		if errors.Is(err, unix.EEXIST) {
			return ErrDesiredChanged
		}
		if err != nil {
			return fmt.Errorf("link desired configuration: %w", err)
		}
		removeTemporary = false
		return syncDirectory(directory)
	}

	validated := current.metadata
	if err := current.stillAt(clean, expectedSourceDigest, validated); err != nil {
		return err
	}
	if beforeDesiredExchange != nil {
		beforeDesiredExchange(clean)
	}
	err = unix.RenameatxNp(unix.AT_FDCWD, temporary, unix.AT_FDCWD, clean, unix.RENAME_SWAP)
	if errors.Is(err, unix.ENOENT) {
		return ErrDesiredChanged
	}
	if err != nil {
		return fmt.Errorf("exchange desired configuration: %w", err)
	}
	// The prior version now lives at the temporary path. Prove it is exactly
	// the inode and bytes validated above before letting it go.
	if err := current.stillAt(temporary, expectedSourceDigest, validated); err != nil {
		if undo := unix.RenameatxNp(unix.AT_FDCWD, temporary, unix.AT_FDCWD, clean, unix.RENAME_SWAP); undo != nil {
			removeTemporary = false
			return fmt.Errorf("%w; the concurrent version is preserved at %s because restoring it failed: %v", err, temporary, undo)
		}
		return err
	}
	removeTemporary = false
	if err := os.Remove(temporary); err != nil {
		return fmt.Errorf("discard superseded configuration: %w", err)
	}
	return syncDirectory(directory)
}

// stillAt proves that path names this very inode and that the inode still
// passes the private-file rules with the same size, modification time, and
// digest observed at validation. Any deviation is reported as ErrDesiredChanged.
func (file *privateRegularFile) stillAt(path string, expectedDigest string, validated unix.Stat_t) error {
	var named unix.Stat_t
	if err := unix.Lstat(path, &named); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return ErrDesiredChanged
		}
		return fmt.Errorf("inspect desired configuration: %w", err)
	}
	if named.Dev != validated.Dev || named.Ino != validated.Ino {
		return ErrDesiredChanged
	}
	return file.unchangedSince(expectedDigest, validated)
}

// unchangedSince re-validates the open inode and re-reads its bytes through
// the held descriptor, refusing any change of identity, mode, owner, link
// count, size, modification time, or digest since validated.
func (file *privateRegularFile) unchangedSince(expectedDigest string, validated unix.Stat_t) error {
	if err := file.revalidate(); err != nil {
		return err
	}
	now := file.metadata
	if now.Dev != validated.Dev || now.Ino != validated.Ino || now.Size != validated.Size || now.Mtim != validated.Mtim {
		return ErrDesiredChanged
	}
	contents, err := file.ReadAll()
	if err != nil {
		return err
	}
	if digest(contents) != expectedDigest {
		return ErrDesiredChanged
	}
	return nil
}

func ensurePrivateDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return fmt.Errorf("create configuration directory: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("restrict configuration directory: %w", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return fmt.Errorf("inspect configuration directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("configuration directory must be a private directory with mode 0700")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("configuration directory must be owned by the current user")
	}
	return nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open configuration directory: %w", err)
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return fmt.Errorf("sync configuration directory: %w", err)
	}
	return nil
}
