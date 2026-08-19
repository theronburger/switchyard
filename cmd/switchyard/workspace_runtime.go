package main

import (
	"context"
	"net/http"
	"path/filepath"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/state"
)

type managedWorkspaceSnapshotStore interface {
	ReadSnapshot(context.Context) (contractv1.StatusSnapshot, error)
}

type managedWorkspaceResolver struct {
	store        managedWorkspaceSnapshotStore
	repositories map[string]contractv1.Repository
	worktrees    map[string]managedWorkspaceTarget
}

type managedWorkspaceTarget struct {
	repositoryID string
	path         string
	primary      bool
}

func newManagedWorkspaceResolver(
	store managedWorkspaceSnapshotStore,
	discovered repositoryInventory,
) managedWorkspaceResolver {
	resolver := managedWorkspaceResolver{
		store: store, repositories: make(map[string]contractv1.Repository),
		worktrees: make(map[string]managedWorkspaceTarget),
	}
	for _, repository := range discovered.Repositories {
		resolver.repositories[repository.ID] = repository
		for _, worktree := range repository.Worktrees {
			resolver.worktrees[worktree.ID] = managedWorkspaceTarget{
				repositoryID: repository.ID, path: worktree.Path, primary: worktree.IsPrimary,
			}
		}
	}
	return resolver
}

func (resolver managedWorkspaceResolver) ResolveCreate(
	_ context.Context,
	request contractv1.CreateWorktreeRequest,
) (workspacecontrol.CreateManagedRequest, error) {
	if _, found := resolver.repositories[request.RepositoryID]; !found {
		return workspacecontrol.CreateManagedRequest{}, workspaceActionError(
			http.StatusNotFound, "REPOSITORY_NOT_FOUND", "The requested repository is not available.",
		)
	}
	return workspacecontrol.CreateManagedRequest{
		RepositoryID: request.RepositoryID, Branch: request.Branch, StartPoint: request.StartPoint,
	}, nil
}

func (resolver managedWorkspaceResolver) ResolveArchive(
	ctx context.Context,
	request contractv1.ArchiveWorktreeRequest,
) (workspacecontrol.ArchiveManagedRequest, error) {
	target, found := resolver.worktrees[request.WorktreeID]
	if !found {
		return workspacecontrol.ArchiveManagedRequest{}, workspaceActionError(
			http.StatusNotFound, "WORKTREE_NOT_FOUND", "The requested worktree is not available.",
		)
	}
	if target.primary {
		return workspacecontrol.ArchiveManagedRequest{}, workspaceActionError(
			http.StatusConflict, "PRIMARY_WORKTREE_PROTECTED", "The primary checkout cannot be archived.",
		)
	}
	if resolver.store == nil {
		return workspacecontrol.ArchiveManagedRequest{}, workspaceActionError(
			http.StatusServiceUnavailable, "WORKSPACE_STATE_UNAVAILABLE", "Workspace state is temporarily unavailable.",
		)
	}
	snapshot, err := resolver.store.ReadSnapshot(ctx)
	if err != nil {
		return workspacecontrol.ArchiveManagedRequest{}, workspaceActionError(
			http.StatusServiceUnavailable, "WORKSPACE_STATE_UNAVAILABLE", "Workspace state is temporarily unavailable.",
		)
	}
	for _, environment := range snapshot.Environments {
		if environment.WorktreeID == request.WorktreeID &&
			(environment.DesiredState != "stopped" || environment.ObservedState != "stopped") {
			return workspacecontrol.ArchiveManagedRequest{}, workspaceActionError(
				http.StatusConflict, "WORKTREE_ENVIRONMENT_ACTIVE", "Stop this worktree's environment before archiving it.",
			)
		}
	}
	return workspacecontrol.ArchiveManagedRequest{
		RepositoryID: target.repositoryID, WorktreePath: target.path,
	}, nil
}

func (resolver managedWorkspaceResolver) ResolveAdopt(
	_ context.Context,
	request contractv1.AdoptWorktreeRequest,
) (workspacecontrol.AdoptManagedRequest, error) {
	target, found := resolver.worktrees[request.WorktreeID]
	if !found {
		return workspacecontrol.AdoptManagedRequest{}, workspaceActionError(
			http.StatusNotFound, "WORKTREE_NOT_FOUND", "The requested worktree is not available.",
		)
	}
	if target.primary {
		return workspacecontrol.AdoptManagedRequest{}, workspaceActionError(
			http.StatusConflict, "PRIMARY_WORKTREE_PROTECTED", "The primary checkout cannot be adopted.",
		)
	}
	return workspacecontrol.AdoptManagedRequest{
		RepositoryID: target.repositoryID, WorktreePath: target.path,
	}, nil
}

func newManagedWorkspaceManager(
	paths applicationPaths,
	discovered repositoryInventory,
) (*workspacecontrol.ManagedManager, error) {
	repositories := make([]workspacecontrol.ManagedRepository, 0, len(discovered.Repositories))
	for _, repository := range discovered.Repositories {
		configuration := discovered.Configurations[repository.ID]
		managedRoot := configuration.Workspace.ManagedRoot
		if managedRoot == "" {
			managedRoot = filepath.Join(
				filepath.Dir(repository.RootPath), filepath.Base(repository.RootPath)+"-worktrees",
			)
		}
		defaultBase := configuration.Workspace.DefaultBase
		if defaultBase == "" {
			defaultBase = "origin/main"
		}
		repositories = append(repositories, workspacecontrol.ManagedRepository{
			ID: repository.ID, Root: repository.RootPath,
			ManagedRoot: managedRoot, DefaultBase: defaultBase,
		})
	}
	return workspacecontrol.NewManagedManager(workspacecontrol.ManagedConfig{
		GitExecutable: configuredGitExecutable(),
		OwnershipRoot: filepath.Join(paths.directory, "runtime", "managed-workspaces"),
		Repositories:  repositories,
	})
}

func annotateWorkspaceInventory(
	paths applicationPaths,
	discovered *repositoryInventory,
) error {
	if len(discovered.Repositories) == 0 {
		return nil
	}
	manager, err := newManagedWorkspaceManager(paths, *discovered)
	if err != nil {
		return err
	}
	for repositoryIndex := range discovered.Repositories {
		repository := &discovered.Repositories[repositoryIndex]
		for worktreeIndex := range repository.Worktrees {
			worktree := &repository.Worktrees[worktreeIndex]
			ownership := "adopted"
			if manager.Owns(repository.ID, worktree.Path) {
				ownership = "managed"
			}
			worktree.Workspace = &contractv1.WorkspaceStatus{
				Ownership: ownership, State: "unprepared", Toolchains: []contractv1.WorkspaceToolchain{},
			}
		}
	}
	return nil
}

func workspaceActionError(status int, code string, message string) error {
	return &daemon.ActionError{Status: status, Contract: contractv1.ContractError{
		Code: code, Message: message,
	}}
}

var _ managedWorkspaceSnapshotStore = (*state.Store)(nil)
