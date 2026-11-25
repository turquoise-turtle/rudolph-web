package dynamo

import (
	"context"
	"fmt"
	"strconv"
	"time"

	rudolphdynamodb "github.com/airbnb/rudolph/pkg/dynamodb"
	"github.com/airbnb/rudolph/pkg/model/fleethealth"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/rudolphrules"
)

// "Run the fleet job now" and "Run the ranker now": each job has a marker of
// its last request, which keeps it from being started more than once every
// interval. The markers are the web view's own items, beside Rudolph's fleet
// health setting, in a partition the editing role can already write and
// Rudolph reads by key only (FleetHealth / Config), so neither job ever sees
// them. The ranker's is here too, not with its lists: those are on the events
// table, which the web view's role only reads.
//
//	PK: FleetHealth, SK: RunRequested (the fleet job) or RankerRunRequested
//	RequestedAt   the request's time, RFC 3339 in UTC to the second
//	ExpiresAfter  the store table's TTL attribute, so the item cleans itself up
const (
	skFleetRunRequested  = "RunRequested"
	skRankerRunRequested = "RankerRunRequested"
	// runMarkerTTL only tidies up. DynamoDB deletes expired items up to days
	// late, so the guard reads RequestedAt and never whether the item is
	// still there.
	runMarkerTTL = time.Hour
)

func runMarkerKey(sortKey string) rudolphdynamodb.PrimaryKey {
	return rudolphdynamodb.PrimaryKey{PartitionKey: fleethealth.PartitionKey, SortKey: sortKey}
}

// RequestFleetRun implements store.Writer. The job's results are the fleet
// snapshot the pointer names.
func (s *Store) RequestFleetRun(ctx context.Context, actor store.Actor, now time.Time, start func(context.Context) error) error {
	return s.requestRun(ctx, rudolphrules.FleetRun, skFleetRunRequested, actor, now, start, func(ctx context.Context) (time.Time, error) {
		sum, err := s.FleetSummary(ctx)
		return sum.GeneratedAt, err
	})
}

// RequestRankerRun implements store.Writer. The ranker's results are the top
// lists; one run stamps every pointer with the same GeneratedAt and moves
// that of the blocked list over 1 day first, so that one stands for all.
func (s *Store) RequestRankerRun(ctx context.Context, actor store.Actor, now time.Time, start func(context.Context) error) error {
	return s.requestRun(ctx, rudolphrules.RankerRun, skRankerRunRequested, actor, now, start, s.topListsGeneratedAt)
}

// topListsGeneratedAt reads when the ranker last moved its first pointer;
// zero when it has not yet.
func (s *Store) topListsGeneratedAt(ctx context.Context) (time.Time, error) {
	// A deployment without an events table shows no top lists, so there is
	// nothing to be fresh. The run is still allowed (owner's decision): the
	// ranker is Rudolph's and may well have its table, and the marker alone
	// keeps the button to one start per interval.
	if s.eventsTable == "" {
		return time.Time{}, nil
	}
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.eventsTable),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: topList{kind: topKindNames[store.TopKindBlocked], days: 1}.pointerPK()},
			"SK": &types.AttributeValueMemberS{Value: skTopPointer},
		},
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("dynamo: reading top list pointer: %w", err)
	}
	// A pointer without a time that can be read says nothing about how fresh
	// the lists are, and a run is what replaces it, so it refuses nothing.
	v, ok := out.Item[attrTopGeneratedAt].(*types.AttributeValueMemberS)
	if !ok {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, v.Value)
	if err != nil {
		return time.Time{}, nil
	}
	return at, nil
}

// requestRun guards, starts and records a run of one of Rudolph's jobs. It
// takes its clock from the caller, unlike the writes made through Rudolph's
// packages: the marker is the web view's own, and the audit entry is still
// stamped by Rudolph's. resultsAt reads the time of the job's current
// results, zero when it has none.
func (s *Store) requestRun(ctx context.Context, job rudolphrules.JobRun, markerSK string, actor store.Actor, now time.Time,
	start func(context.Context) error, resultsAt func(context.Context) (time.Time, error)) error {
	who, err := actor.AuditName()
	if err != nil {
		return err
	}
	c, err := s.rudolph(ctx)
	if err != nil {
		return err
	}
	key := runMarkerKey(markerSK)
	results, err := resultsAt(ctx)
	if err != nil {
		return err
	}
	if _, fresh := job.RetryAt(now, time.Time{}, results); fresh {
		// Refused whatever the marker says, so nothing is written. The marker
		// is read only to name the later of the two times, and the results'
		// is right enough without it.
		requested, _ := runRequestedAt(c, key)
		retryAt, _ := job.RetryAt(now, requested, results)
		return job.TooSoon(retryAt)
	}

	// One conditional write decides between requests made at once: a read
	// followed by a plain write would let both start the job. RFC 3339 in UTC
	// to the second is fixed-width, so the strings compare as the times do. A
	// marker that is no time, or is further ahead than a request could have
	// written it, is replaced rather than left to refuse every request.
	_, err = c.UpdateItemExpression(key, "SET RequestedAt = :at, ExpiresAfter = :expires",
		"attribute_not_exists(RequestedAt) OR NOT attribute_type(RequestedAt, :string) OR RequestedAt <= :cutoff OR RequestedAt > :ahead",
		nil, map[string]types.AttributeValue{
			":at":      &types.AttributeValueMemberS{Value: now.UTC().Format(time.RFC3339)},
			":expires": &types.AttributeValueMemberN{Value: strconv.FormatInt(now.Add(runMarkerTTL).Unix(), 10)},
			":string":  &types.AttributeValueMemberS{Value: "S"},
			":cutoff":  &types.AttributeValueMemberS{Value: now.Add(-job.Interval).UTC().Format(time.RFC3339)},
			":ahead":   &types.AttributeValueMemberS{Value: now.Add(job.Interval).UTC().Format(time.RFC3339)},
		})
	if rudolphdynamodb.IsConditionalCheckFailed(err) {
		// When the marker cannot be read back, a full interval from now is the
		// longest the wait can be.
		retryAt := now.Add(job.Interval)
		if requested, err := runRequestedAt(c, key); err == nil {
			if at, soon := job.RetryAt(now, requested, time.Time{}); soon {
				retryAt = at
			}
		}
		return job.TooSoon(retryAt)
	}
	if err != nil {
		return fmt.Errorf("dynamo: marking the %s run request: %w", job.Target, err)
	}

	// The marker stays when the start fails, so a job that cannot be started
	// is not asked for on every click.
	if err := start(ctx); err != nil {
		return fmt.Errorf("%w: %w", job.NotStarted, err)
	}
	return s.record(ctx, c, who, job.Action, job.Target, job.Summary)
}

// runRequestedAt reads when a job's last request was accepted with one
// strongly consistent GetItem; zero when there is no marker or it holds no
// time.
func runRequestedAt(c RudolphClient, key rudolphdynamodb.PrimaryKey) (time.Time, error) {
	out, err := c.GetItem(key, true)
	if err != nil {
		return time.Time{}, fmt.Errorf("dynamo: reading the run request: %w", err)
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
