package inventory

import "context"

type RepositoryReader interface {
	ReadRepository(context.Context, string) RepositoryObservation
}

type RepositoryObservation struct {
	AdapterName       string
	CommonDirectory   string
	SharedExcludePath string
	Remote            string
	Worktrees         []WorktreeObservation
	Alerts            []AlertObservation
	Errors            []ErrorObservation
}

type WorktreeObservation struct {
	Path                   string
	AdministrativeIdentity string
	Branch                 string
	HeadRevision           string
	IsPrimary              bool
	Detached               bool
	Bare                   bool
	Locked                 bool
	Prunable               bool
}

type AlertObservation struct {
	Code         AlertCode
	WorktreePath string
}

type ErrorObservation struct {
	Code         ErrorCode
	WorktreePath string
}
