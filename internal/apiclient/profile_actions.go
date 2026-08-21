package apiclient

import (
	"context"
	"fmt"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

const maximumActionListBytes = int64(4 * 1024 * 1024)

func (c *Client) ListProfileActions(ctx context.Context) (contractv2.ProfileActionList, error) {
	if _, err := c.Handshake(ctx); err != nil {
		return contractv2.ProfileActionList{}, err
	}
	var list contractv2.ProfileActionList
	if err := c.getJSON(ctx, "/v1/actions", maximumActionListBytes, &list); err != nil {
		return contractv2.ProfileActionList{}, err
	}
	if err := list.Validate(); err != nil {
		return contractv2.ProfileActionList{}, newCodedError(ErrorDaemonResponseInvalid, err)
	}
	return list, nil
}

func (c *Client) RunProfileAction(
	ctx context.Context,
	request contractv2.RunProfileActionRequest,
) (contractv2.MutationReceipt, error) {
	if request.Validate() != nil {
		return contractv2.MutationReceipt{}, newCodedError(
			ErrorActionRequestInvalid, fmt.Errorf("profile action request is invalid"),
		)
	}
	if _, err := c.Handshake(ctx); err != nil {
		return contractv2.MutationReceipt{}, err
	}
	return c.postMutation(ctx, "/v1/actions/run", request)
}

func (c Connector) ListProfileActions(ctx context.Context) (contractv2.ProfileActionList, error) {
	client, err := c.Client()
	if err != nil {
		return contractv2.ProfileActionList{}, err
	}
	return client.ListProfileActions(ctx)
}

func (c Connector) RunProfileAction(
	ctx context.Context,
	request contractv2.RunProfileActionRequest,
) (contractv2.MutationReceipt, error) {
	client, err := c.Client()
	if err != nil {
		return contractv2.MutationReceipt{}, err
	}
	return client.RunProfileAction(ctx, request)
}
