package dynamo

import (
	"context"
	"fmt"
	"strconv"
	"time"

	rudolphdynamodb "github.com/airbnb/rudolph/pkg/dynamodb"
	"github.com/airbnb/rudolph/pkg/model/fleethealth"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// "Run the fleet job now": the marker of the last request, which keeps the
// job from being started more than once every store.FleetRunInterval. It is
// the web view's own item, beside Rudolph's fleet health setting, in a
// partition the editing role can already write and the fleet job reads by
// key only (FleetHealth / Config), so the job never sees it:
//
//	PK: FleetHealth, SK: RunRequested
//	RequestedAt   the request's time, RFC 3339 in UTC to the second
//	ExpiresAfter  the store table's TTL attribute, so the item cleans itself up
const (
	skFleetRunRequested = "RunRequested"
	// fleetRunMarkerTTL only tidies up. DynamoDB deletes expired items up to
	// days late, so the guard reads RequestedAt and never whether the item is
	// still there.
	fleetRunMarkerTTL = time.Hour
)

func fleetRunMarkerKey() rudolphdynamodb.PrimaryKey {
	return rudolphdynamodb.PrimaryKey{PartitionKey: fleethealth.PartitionKey, SortKey: skFleetRunRequested}
}

// RequestFleetRun implements store.Writer. It takes its clock from the
// caller, unlike the writes made through Rudolph's packages: the marker is
// the web view's own, and the audit entry is still stamped by Rudolph's.
func (s *Store) RequestFleetRun(ctx context.Context, actor store.Actor, now time.Time, start func(context.Context) error) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	sum, err := s.FleetSummary(ctx)
	if err != nil {
		return err
	}
	if _, fresh := store.FleetRunRetryAt(now, time.Time{}, sum.GeneratedAt); fresh {
		// Refused whatever the marker says, so nothing is written. The marker
		// is read only to name the later of the two times, and the snapshot's
		// is right enough without it.
		requested, _ := s.fleetRunRequestedAt(c)
		retryAt, _ := store.FleetRunRetryAt(now, requested, sum.GeneratedAt)
		return &store.FleetRunTooSoonError{RetryAt: retryAt}
	}

	// One conditional write decides between requests made at once: a read
	// followed by a plain write would let both start the job. RFC 3339 in UTC
	// to the second is fixed-width, so the strings compare as the times do. A
	// marker that is no time, or is further ahead than a request could have
	// written it, is replaced rather than left to refuse every request.
	_, err = c.UpdateItemExpression(fleetRunMarkerKey(), "SET RequestedAt = :at, ExpiresAfter = :expires",
		"attribute_not_exists(RequestedAt) OR NOT attribute_type(RequestedAt, :string) OR RequestedAt <= :cutoff OR RequestedAt > :ahead",
		nil, map[string]types.AttributeValue{
			":at":      &types.AttributeValueMemberS{Value: now.UTC().Format(time.RFC3339)},
			":expires": &types.AttributeValueMemberN{Value: strconv.FormatInt(now.Add(fleetRunMarkerTTL).Unix(), 10)},
			":string":  &types.AttributeValueMemberS{Value: "S"},
			":cutoff":  &types.AttributeValueMemberS{Value: now.Add(-store.FleetRunInterval).UTC().Format(time.RFC3339)},
			":ahead":   &types.AttributeValueMemberS{Value: now.Add(store.FleetRunInterval).UTC().Format(time.RFC3339)},
		})
	if rudolphdynamodb.IsConditionalCheckFailed(err) {
		// When the marker cannot be read back, a full interval from now is the
		// longest the wait can be.
		retryAt := now.Add(store.FleetRunInterval)
		if requested, err := s.fleetRunRequestedAt(c); err == nil {
			if at, soon := store.FleetRunRetryAt(now, requested, time.Time{}); soon {
				retryAt = at
			}
		}
		return &store.FleetRunTooSoonError{RetryAt: retryAt}
	}
	if err != nil {
		return fmt.Errorf("dynamo: marking the fleet run request: %w", err)
	}

	// The marker stays when the start fails, so a job that cannot be started
	// is not asked for on every click.
	if err := start(ctx); err != nil {
		return fmt.Errorf("%w: %w", store.ErrFleetRunNotStarted, err)
	}
	return s.record(ctx, c, who, store.ActionFleetRun, store.AuditTargetFleet, rudolphrules.FleetRunSummary)
}

// fleetRunRequestedAt reads when the last request was accepted with one
// strongly consistent GetItem; zero when there is no marker or it holds no
// time.
func (s *Store) fleetRunRequestedAt(c RudolphClient) (time.Time, error) {
	out, err := c.GetItem(fleetRunMarkerKey(), true)
	if err != nil {
		return time.Time{}, fmt.Errorf("dynamo: reading the fleet run request: %w", err)
	}
	v, ok := out.Item["RequestedAt"].(*types.AttributeValueMemberS)
	if !ok {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339, v.Value)
	if err != nil {
		return time.Time{}, nil
	}
	return at, nil
}
