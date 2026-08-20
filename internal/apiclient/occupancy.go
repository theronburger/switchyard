package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

// AcquireOccupancy records an explicit handoff lease for a worktree. The
// daemon is the only lease writer; this client never infers occupancy.
func (c *Client) AcquireOccupancy(ctx context.Context, request contractv2.AcquireOccupancyRequest) (contractv2.OccupancyLease, error) {
	if request.Validate() != nil || !safePathSegment(request.WorktreeID) {
		return contractv2.OccupancyLease{}, newCodedError(ErrorActionRequestInvalid, fmt.Errorf("occupancy request is invalid"))
	}
	var lease contractv2.OccupancyLease
	path := "/v1/worktrees/" + url.PathEscape(request.WorktreeID) + "/occupancy"
	if err := c.postCleanup(ctx, path, request, http.StatusOK, &lease); err != nil {
		return contractv2.OccupancyLease{}, err
	}
	if lease.Validate() != nil || lease.WorktreeID != request.WorktreeID || lease.State != "held" {
		return contractv2.OccupancyLease{}, newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("occupancy lease is invalid"))
	}
	return lease, nil
}

// ReleaseOccupancy ends a handoff lease. Releasing an already released lease
// succeeds and returns the released record.
func (c *Client) ReleaseOccupancy(ctx context.Context, request contractv2.ReleaseOccupancyRequest) (contractv2.OccupancyLease, error) {
	if request.Validate() != nil || !safePathSegment(request.WorktreeID) || !safePathSegment(request.LeaseID) {
		return contractv2.OccupancyLease{}, newCodedError(ErrorActionRequestInvalid, fmt.Errorf("occupancy request is invalid"))
	}
	var lease contractv2.OccupancyLease
	path := "/v1/worktrees/" + url.PathEscape(request.WorktreeID) + "/occupancy/" + url.PathEscape(request.LeaseID) + "/release"
	if err := c.postCleanup(ctx, path, request, http.StatusOK, &lease); err != nil {
		return contractv2.OccupancyLease{}, err
	}
	if lease.Validate() != nil || lease.WorktreeID != request.WorktreeID || lease.ID != request.LeaseID || lease.State != "released" {
		return contractv2.OccupancyLease{}, newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("occupancy lease is invalid"))
	}
	return lease, nil
}

func (c Connector) AcquireOccupancy(ctx context.Context, request contractv2.AcquireOccupancyRequest) (contractv2.OccupancyLease, error) {
	client, err := c.Client()
	if err != nil {
		return contractv2.OccupancyLease{}, err
	}
	return client.AcquireOccupancy(ctx, request)
}

func (c Connector) ReleaseOccupancy(ctx context.Context, request contractv2.ReleaseOccupancyRequest) (contractv2.OccupancyLease, error) {
	client, err := c.Client()
	if err != nil {
		return contractv2.OccupancyLease{}, err
	}
	return client.ReleaseOccupancy(ctx, request)
}

func safePathSegment(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if character == '/' || character == '\\' || character == '?' || character == '#' || character == '%' || character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}
