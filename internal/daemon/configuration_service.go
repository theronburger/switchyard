package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

type ConfigurationStore interface {
	StageConfiguration(context.Context, int64, string, configuration.Loaded) (state.ConfigurationCandidate, error)
	AcceptConfiguration(context.Context, int64, string) (state.ConfigurationRevision, error)
	ReadAcceptedConfiguration(context.Context) (state.ConfigurationRevision, error)
}

type ConfigurationService struct {
	Store           ConfigurationStore
	Path            string
	CompilerVersion string
	Restart         func()
}

func (service ConfigurationService) Status(ctx context.Context) (contractv2.ConfigurationStatus, error) {
	accepted, err := service.Store.ReadAcceptedConfiguration(ctx)
	if errors.Is(err, state.ErrConfigurationNotAccepted) {
		return contractv2.ConfigurationStatus{SchemaVersion: contractv2.SchemaVersion, State: "missing"}, nil
	}
	if err != nil {
		return contractv2.ConfigurationStatus{}, err
	}
	return contractv2.ConfigurationStatus{
		SchemaVersion: contractv2.SchemaVersion, State: "accepted",
		AcceptedRevision: accepted.Revision, AcceptedDigest: accepted.Digest,
	}, nil
}

func (service ConfigurationService) Validate(
	ctx context.Context,
	request contractv2.ConfigurationValidationRequest,
) (contractv2.ConfigurationStatus, error) {
	if request.Validate() != nil || service.Store == nil || service.Path == "" || service.CompilerVersion == "" {
		return contractv2.ConfigurationStatus{}, errors.New("configuration service is unavailable")
	}
	loaded, err := configuration.LoadFile(service.Path)
	if err != nil {
		return contractv2.ConfigurationStatus{}, err
	}
	candidate, err := service.Store.StageConfiguration(ctx, request.ExpectedRevision, service.CompilerVersion, loaded)
	if err != nil {
		return contractv2.ConfigurationStatus{}, err
	}
	status := contractv2.ConfigurationStatus{
		SchemaVersion: contractv2.SchemaVersion, State: "pending",
		AcceptedRevision: request.ExpectedRevision,
		Candidate:        candidateContract(candidate),
	}
	if accepted, readErr := service.Store.ReadAcceptedConfiguration(ctx); readErr == nil {
		status.AcceptedDigest = accepted.Digest
		if accepted.Digest == candidate.Digest {
			status.State = "accepted"
			status.Candidate = nil
		}
	}
	return status, nil
}

func (service ConfigurationService) Accept(
	ctx context.Context,
	request contractv2.ConfigurationAcceptanceRequest,
) (contractv2.ConfigurationStatus, error) {
	if request.Validate() != nil || service.Store == nil {
		return contractv2.ConfigurationStatus{}, errors.New("configuration service is unavailable")
	}
	accepted, err := service.Store.AcceptConfiguration(ctx, request.ExpectedRevision, request.Digest)
	if err != nil {
		return contractv2.ConfigurationStatus{}, err
	}
	status := contractv2.ConfigurationStatus{
		SchemaVersion: contractv2.SchemaVersion, State: "accepted",
		AcceptedRevision: accepted.Revision, AcceptedDigest: accepted.Digest,
	}
	if service.Restart != nil {
		time.AfterFunc(10*time.Millisecond, service.Restart)
	}
	return status, nil
}

func candidateContract(candidate state.ConfigurationCandidate) *contractv2.ConfigurationCandidate {
	return &contractv2.ConfigurationCandidate{
		SchemaVersion: contractv2.SchemaVersion, Digest: candidate.Digest,
		SourceDigest: candidate.SourceDigest, CompilerVersion: candidate.CompilerVersion,
		RepositoryDigests: candidate.RepositoryDigests, StagedAt: candidate.StagedAt,
		ExecutableDigests: candidate.ExecutableDigests,
	}
}
