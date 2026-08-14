package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNoSnapshot  = errors.New("no status snapshot")
	ErrStoreLocked = errors.New("state store is owned by another daemon")
)

type Config struct {
	Path string
	Now  func() time.Time
}

type Store struct {
	database  *sql.DB
	lockFile  *os.File
	now       func() time.Time
	closeOnce sync.Once
	closeErr  error
}

func Open(ctx context.Context, config Config) (*Store, error) {
	if strings.TrimSpace(config.Path) == "" {
		return nil, errors.New("state database path is required")
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	if err := os.MkdirAll(filepath.Dir(config.Path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	lockFile, err := acquireStoreLock(config.Path + ".lock")
	if err != nil {
		return nil, err
	}

	closeLock := true
	defer func() {
		if closeLock {
			_ = releaseStoreLock(lockFile)
		}
	}()

	if err := ensurePrivateDatabaseFile(config.Path); err != nil {
		return nil, err
	}

	database, err := sql.Open("sqlite", config.Path)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	store := &Store{database: database, lockFile: lockFile, now: config.Now}
	if err := store.configure(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}

	closeLock = false
	return store, nil
}

func (store *Store) Close() error {
	store.closeOnce.Do(func() {
		databaseErr := store.database.Close()
		lockErr := releaseStoreLock(store.lockFile)
		store.closeErr = errors.Join(databaseErr, lockErr)
	})
	return store.closeErr
}

func (store *Store) configure(ctx context.Context) error {
	var journalMode string
	if err := store.database.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("enable WAL journal: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("enable WAL journal: sqlite selected %q", journalMode)
	}

	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := store.database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite with %q: %w", statement, err)
		}
	}
	return nil
}

func ensurePrivateDatabaseFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create state database: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect state database: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close state database file: %w", err)
	}
	return nil
}

func acquireStoreLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open state lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect state lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrStoreLocked
		}
		return nil, fmt.Errorf("lock state store: %w", err)
	}
	return file, nil
}

func releaseStoreLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
