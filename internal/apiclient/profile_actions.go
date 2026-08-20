package apiclient

import (
	"context"
	"fmt"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

const maximumActionListBytes = int64(4 * 1024 * 1024)

func (c *Client) ListProfileActions(ctx context.Context) (contractv1.ProfileActionList, error) {
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.ProfileActionList{}, err
	}
	var list contractv1.ProfileActionList
	if err := c.getJSON(ctx, "/v1/actions", maximumActionListBytes, &list); err != nil {
		return contractv1.ProfileActionList{}, err
	}
	if err := list.Validate(); err != nil {
		return contractv1.ProfileActionList{}, newCodedError(ErrorDaemonResponseInvalid, err)
	}
	return list, nil
}

func (c *Client) RunProfileAction(
	ctx context.Context,
	request contractv1.RunProfileActionRequest,
) (contractv1.MutationReceipt, error) {
	if request.Validate() != nil {
		return contractv1.MutationReceipt{}, newCodedError(
			ErrorActionRequestInvalid, fmt.Errorf("profile action request is invalid"),
		)
	}
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return c.postMutation(ctx, "/v1/actions/run", request)
}

func (c Connector) ListProfileActions(ctx context.Context) (contractv1.ProfileActionList, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.ProfileActionList{}, err
	}
	return client.ListProfileActions(ctx)
}

func (c Connector) RunProfileAction(
	ctx context.Context,
	request contractv1.RunProfileActionRequest,
) (contractv1.MutationReceipt, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.MutationReceipt{}, err
	}
	return client.RunProfileAction(ctx, request)
}
