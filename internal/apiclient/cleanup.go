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

func (c *Client) PlanCleanup(ctx context.Context, value contractv1.CleanupPlanRequest) (contractv1.CleanupPlan, error) {
	if value.Validate() != nil {
		return contractv1.CleanupPlan{}, newCodedError(ErrorActionRequestInvalid, fmt.Errorf("cleanup plan request is invalid"))
	}
	var plan contractv1.CleanupPlan
	if err := c.postCleanup(ctx, "/v1/cleanup/plans", value, http.StatusCreated, &plan); err != nil {
		return contractv1.CleanupPlan{}, err
	}
	if plan.Validate() != nil {
		return contractv1.CleanupPlan{}, newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("cleanup plan is invalid"))
	}
	return plan, nil
}

func (c *Client) ApplyCleanup(ctx context.Context, value contractv1.CleanupApplyRequest) (contractv1.CleanupResult, error) {
	if value.Validate() != nil {
		return contractv1.CleanupResult{}, newCodedError(ErrorActionRequestInvalid, fmt.Errorf("cleanup apply request is invalid"))
	}
	var result contractv1.CleanupResult
	if err := c.postCleanup(ctx, "/v1/cleanup/plans/"+value.PlanID+"/apply", value, http.StatusOK, &result); err != nil {
		return contractv1.CleanupResult{}, err
	}
	if result.Validate() != nil {
		return contractv1.CleanupResult{}, newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("cleanup result is invalid"))
	}
	return result, nil
}

func (c *Client) postCleanup(ctx context.Context, path string, value any, success int, destination any) error {
	if _, err := c.Handshake(ctx); err != nil {
		return err
	}
	contents, err := json.Marshal(value)
	if err != nil {
		return newCodedError(ErrorActionRequestInvalid, err)
	}
	requestURL := *c.connection.endpoint
	requestURL.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(contents))
	if err != nil {
		return newCodedError(ErrorDaemonUnavailable, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.connection.token)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return newCodedError(ErrorDaemonUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumStatusBytes+1))
	if err != nil || len(payload) > maximumStatusBytes || !secureResponseHeaders(response) {
		return newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("cleanup response is invalid"))
	}
	if response.StatusCode == success {
		if decodeSingleJSON(payload, destination) != nil {
			return newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("cleanup response is invalid"))
		}
		return nil
	}
	var failure mutationErrorResponse
	if decodeSingleJSON(payload, &failure) != nil || failure.SchemaVersion != contractv1.SchemaVersion ||
		failure.Error.Code == "" || failure.Error.Message == "" {
		return newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("cleanup error is invalid"))
	}
	return newContractError(failure.Error, fmt.Errorf("daemon rejected cleanup request"))
}

func (c Connector) PlanCleanup(ctx context.Context, request contractv1.CleanupPlanRequest) (contractv1.CleanupPlan, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.CleanupPlan{}, err
	}
	return client.PlanCleanup(ctx, request)
}

func (c Connector) ApplyCleanup(ctx context.Context, request contractv1.CleanupApplyRequest) (contractv1.CleanupResult, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.CleanupResult{}, err
	}
	return client.ApplyCleanup(ctx, request)
}
