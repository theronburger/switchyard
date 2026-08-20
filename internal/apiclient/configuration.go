package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

func (c *Client) Configuration(ctx context.Context) (contractv1.ConfigurationStatus, error) {
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.ConfigurationStatus{}, err
	}
	var status contractv1.ConfigurationStatus
	if err := c.getJSON(ctx, "/v1/configuration", maximumMutationResponseBytes, &status); err != nil {
		return contractv1.ConfigurationStatus{}, err
	}
	if err := status.Validate(); err != nil {
		return contractv1.ConfigurationStatus{}, newCodedError(ErrorDaemonResponseInvalid, err)
	}
	return status, nil
}

func (c *Client) ValidateConfiguration(
	ctx context.Context,
	request contractv1.ConfigurationValidationRequest,
) (contractv1.ConfigurationStatus, error) {
	if request.Validate() != nil {
		return contractv1.ConfigurationStatus{}, newCodedError(ErrorActionRequestInvalid, fmt.Errorf("configuration validation request is invalid"))
	}
	return c.postConfiguration(ctx, "/v1/configuration/validate", request)
}

func (c *Client) AcceptConfiguration(
	ctx context.Context,
	request contractv1.ConfigurationAcceptanceRequest,
) (contractv1.ConfigurationStatus, error) {
	if request.Validate() != nil {
		return contractv1.ConfigurationStatus{}, newCodedError(ErrorActionRequestInvalid, fmt.Errorf("configuration acceptance request is invalid"))
	}
	return c.postConfiguration(ctx, "/v1/configuration/accept", request)
}

func (c *Client) postConfiguration(ctx context.Context, path string, value any) (contractv1.ConfigurationStatus, error) {
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.ConfigurationStatus{}, err
	}
	contents, err := json.Marshal(value)
	if err != nil {
		return contractv1.ConfigurationStatus{}, newCodedError(ErrorActionRequestInvalid, err)
	}
	requestURL := *c.connection.endpoint
	requestURL.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(contents))
	if err != nil {
		return contractv1.ConfigurationStatus{}, newCodedError(ErrorDaemonUnavailable, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.connection.token)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return contractv1.ConfigurationStatus{}, newCodedError(ErrorDaemonUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	responseContents, err := io.ReadAll(io.LimitReader(response.Body, maximumMutationResponseBytes+1))
	if err != nil || len(responseContents) > maximumMutationResponseBytes || !secureResponseHeaders(response) {
		return contractv1.ConfigurationStatus{}, newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("configuration response is invalid"))
	}
	if response.StatusCode == http.StatusOK {
		var status contractv1.ConfigurationStatus
		if decodeSingleJSON(responseContents, &status) != nil || status.Validate() != nil {
			return contractv1.ConfigurationStatus{}, newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("configuration status is invalid"))
		}
		return status, nil
	}
	var failure mutationErrorResponse
	if decodeSingleJSON(responseContents, &failure) != nil || failure.SchemaVersion != contractv1.SchemaVersion ||
		failure.Error.Code == "" || failure.Error.Message == "" {
		return contractv1.ConfigurationStatus{}, newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("configuration error is invalid"))
	}
	return contractv1.ConfigurationStatus{}, newContractError(failure.Error, fmt.Errorf("daemon rejected configuration request"))
}

func (c Connector) Configuration(ctx context.Context) (contractv1.ConfigurationStatus, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.ConfigurationStatus{}, err
	}
	return client.Configuration(ctx)
}

func (c Connector) ValidateConfiguration(ctx context.Context, request contractv1.ConfigurationValidationRequest) (contractv1.ConfigurationStatus, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.ConfigurationStatus{}, err
	}
	return client.ValidateConfiguration(ctx, request)
}

func (c Connector) AcceptConfiguration(ctx context.Context, request contractv1.ConfigurationAcceptanceRequest) (contractv1.ConfigurationStatus, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.ConfigurationStatus{}, err
	}
	return client.AcceptConfiguration(ctx, request)
}
