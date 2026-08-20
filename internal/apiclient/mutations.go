package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

const maximumMutationResponseBytes = 64 * 1024

type mutationErrorResponse struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Error         contractv1.ContractError `json:"error"`
}

func (c *Client) StartEnvironment(
	ctx context.Context,
	request contractv1.StartEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	if err := request.Validate(); err != nil {
		return contractv1.MutationReceipt{}, newCodedError(ErrorActionRequestInvalid, err)
	}
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return c.postMutation(ctx, "/v1/environments", request)
}

func (c *Client) StopEnvironment(
	ctx context.Context,
	environmentID string,
	request contractv1.StopEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	if !safeMutationPathID(environmentID) || request.Validate() != nil {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorActionRequestInvalid,
			fmt.Errorf("environment stop request is invalid"),
		)
	}
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return c.postMutation(ctx, "/v1/environments/"+environmentID+"/stop", request)
}

func (c *Client) CreateWorktree(
	ctx context.Context,
	request contractv1.CreateWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	if request.Validate() != nil {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorActionRequestInvalid, fmt.Errorf("worktree creation request is invalid"),
		)
	}
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return c.postMutation(ctx, "/v1/worktrees", request)
}

func (c *Client) ArchiveWorktree(
	ctx context.Context,
	request contractv1.ArchiveWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	if !safeMutationPathID(request.WorktreeID) || request.Validate() != nil {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorActionRequestInvalid, fmt.Errorf("worktree archive request is invalid"),
		)
	}
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return c.postMutation(ctx, "/v1/worktrees/"+request.WorktreeID+"/archive", request)
}

func (c *Client) AdoptWorktree(
	ctx context.Context,
	request contractv1.AdoptWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	if !safeMutationPathID(request.WorktreeID) || request.Validate() != nil {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorActionRequestInvalid, fmt.Errorf("worktree adoption request is invalid"),
		)
	}
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return c.postMutation(ctx, "/v1/worktrees/"+request.WorktreeID+"/adopt", request)
}

func (c *Client) PrepareWorktree(
	ctx context.Context,
	request contractv1.PrepareWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	if !safeMutationPathID(request.WorktreeID) || request.Validate() != nil {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorActionRequestInvalid, fmt.Errorf("worktree preparation request is invalid"),
		)
	}
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return c.postMutation(ctx, "/v1/worktrees/"+request.WorktreeID+"/prepare", request)
}

func (c *Client) postMutation(
	ctx context.Context,
	path string,
	mutation any,
) (contractv1.MutationReceipt, error) {
	contents, err := json.Marshal(mutation)
	if err != nil {
		return contractv1.MutationReceipt{}, newCodedError(ErrorActionRequestInvalid, err)
	}
	requestURL := *c.connection.endpoint
	requestURL.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(contents))
	if err != nil {
		return contractv1.MutationReceipt{}, newCodedError(ErrorDaemonUnavailable, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.connection.token)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return contractv1.MutationReceipt{}, newCodedError(ErrorDaemonUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if !secureResponseHeaders(response) {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorDaemonResponseInvalid,
			fmt.Errorf("daemon response is missing required security headers"),
		)
	}
	responseContents, err := io.ReadAll(io.LimitReader(response.Body, maximumMutationResponseBytes+1))
	if err != nil || len(responseContents) > maximumMutationResponseBytes {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorDaemonResponseInvalid,
			fmt.Errorf("daemon mutation response is invalid"),
		)
	}

	if response.StatusCode == http.StatusAccepted {
		var receipt contractv1.MutationReceipt
		if err := decodeSingleJSON(responseContents, &receipt); err != nil || receipt.Validate() != nil {
			return contractv1.MutationReceipt{}, newCodedError(
				ErrorDaemonResponseInvalid,
				fmt.Errorf("daemon mutation receipt is invalid"),
			)
		}
		return receipt, nil
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorDaemonUnauthorized,
			fmt.Errorf("daemon rejected authentication"),
		)
	}

	var failure mutationErrorResponse
	if err := decodeSingleJSON(responseContents, &failure); err != nil ||
		failure.SchemaVersion != contractv1.SchemaVersion ||
		!safeErrorCode(failure.Error.Code) || failure.Error.Message == "" {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorDaemonResponseInvalid,
			fmt.Errorf("daemon mutation error is invalid"),
		)
	}
	return contractv1.MutationReceipt{}, newContractError(
		failure.Error,
		fmt.Errorf("daemon rejected environment mutation"),
	)
}

func (c Connector) StartEnvironment(
	ctx context.Context,
	request contractv1.StartEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return client.StartEnvironment(ctx, request)
}

func (c Connector) StopEnvironment(
	ctx context.Context,
	environmentID string,
	request contractv1.StopEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return client.StopEnvironment(ctx, environmentID, request)
}

func (c Connector) CreateWorktree(
	ctx context.Context,
	request contractv1.CreateWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return client.CreateWorktree(ctx, request)
}

func (c Connector) ArchiveWorktree(
	ctx context.Context,
	request contractv1.ArchiveWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return client.ArchiveWorktree(ctx, request)
}

func (c Connector) AdoptWorktree(
	ctx context.Context,
	request contractv1.AdoptWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return client.AdoptWorktree(ctx, request)
}

func (c Connector) PrepareWorktree(
	ctx context.Context,
	request contractv1.PrepareWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return client.PrepareWorktree(ctx, request)
}

func decodeSingleJSON(contents []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := requireJSONEnd(decoder); err != nil {
		return err
	}
	return nil
}

func secureResponseHeaders(response *http.Response) bool {
	return strings.Contains(strings.ToLower(response.Header.Get("Cache-Control")), "no-store") &&
		strings.EqualFold(response.Header.Get("X-Content-Type-Options"), "nosniff")
}

func safeMutationPathID(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	return !strings.ContainsAny(value, "/?#")
}

func safeErrorCode(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}
