package apiclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

const maximumDiagnosticsResponseBytes = 96 * 1024

func (c *Client) OperationDiagnostics(
	ctx context.Context,
	operationID string,
	maximumBytes int,
) (contractv2.OperationDiagnostics, error) {
	if !safeMutationPathID(operationID) || (maximumBytes != 0 && (maximumBytes < 256 || maximumBytes > 32*1024)) {
		return contractv2.OperationDiagnostics{}, newCodedError(
			ErrorActionRequestInvalid, fmt.Errorf("operation diagnostics request is invalid"),
		)
	}
	if _, err := c.Handshake(ctx); err != nil {
		return contractv2.OperationDiagnostics{}, err
	}
	requestURL := *c.connection.endpoint
	requestURL.Path = "/v1/operations/" + operationID + "/diagnostics"
	if maximumBytes != 0 {
		requestURL.RawQuery = url.Values{"maxBytes": {strconv.Itoa(maximumBytes)}}.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return contractv2.OperationDiagnostics{}, newCodedError(ErrorDaemonUnavailable, err)
	}
	request.Header.Set("Accept", "application/json")
	c.declareContract(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return contractv2.OperationDiagnostics{}, newCodedError(ErrorDaemonUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if !secureResponseHeaders(response) {
		return contractv2.OperationDiagnostics{}, newCodedError(
			ErrorDaemonResponseInvalid, fmt.Errorf("daemon response is missing required security headers"),
		)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maximumDiagnosticsResponseBytes+1))
	if err != nil || len(contents) > maximumDiagnosticsResponseBytes {
		return contractv2.OperationDiagnostics{}, newCodedError(
			ErrorDaemonResponseInvalid, fmt.Errorf("daemon diagnostics response is invalid"),
		)
	}
	if response.StatusCode == http.StatusUpgradeRequired {
		return contractv2.OperationDiagnostics{}, upgradeRequiredFromContents(contents)
	}
	if response.StatusCode != http.StatusOK {
		var failure mutationErrorResponse
		if decodeSingleJSON(contents, &failure) != nil || failure.SchemaVersion != contractv2.SchemaVersion ||
			!safeErrorCode(failure.Error.Code) || failure.Error.Message == "" {
			return contractv2.OperationDiagnostics{}, newCodedError(
				ErrorDaemonResponseInvalid, fmt.Errorf("daemon diagnostics error is invalid"),
			)
		}
		return contractv2.OperationDiagnostics{}, newContractError(
			failure.Error, fmt.Errorf("daemon rejected operation diagnostics request"),
		)
	}
	var diagnostics contractv2.OperationDiagnostics
	if decodeSingleJSON(contents, &diagnostics) != nil || !validOperationDiagnostics(diagnostics, operationID, maximumBytes) {
		return contractv2.OperationDiagnostics{}, newCodedError(
			ErrorDaemonResponseInvalid, fmt.Errorf("daemon diagnostics response is invalid"),
		)
	}
	return diagnostics, nil
}

func (c Connector) OperationDiagnostics(
	ctx context.Context,
	operationID string,
	maximumBytes int,
) (contractv2.OperationDiagnostics, error) {
	client, err := c.Client()
	if err != nil {
		return contractv2.OperationDiagnostics{}, err
	}
	return client.OperationDiagnostics(ctx, operationID, maximumBytes)
}

func validOperationDiagnostics(diagnostics contractv2.OperationDiagnostics, operationID string, maximumBytes int) bool {
	if maximumBytes == 0 {
		maximumBytes = 8 * 1024
	}
	// Repository, worktree, and machine scoped profile actions report
	// diagnostics without an environment.
	if diagnostics.SchemaVersion != contractv2.SchemaVersion || diagnostics.OperationID != operationID ||
		diagnostics.LogReference == "" ||
		len(diagnostics.Excerpts) == 0 || len(diagnostics.Excerpts) > 2 {
		return false
	}
	seen := make(map[string]bool, len(diagnostics.Excerpts))
	for _, excerpt := range diagnostics.Excerpts {
		if (excerpt.Stream != "stdout" && excerpt.Stream != "stderr") || seen[excerpt.Stream] || len(excerpt.Content) > maximumBytes {
			return false
		}
		seen[excerpt.Stream] = true
	}
	return true
}
