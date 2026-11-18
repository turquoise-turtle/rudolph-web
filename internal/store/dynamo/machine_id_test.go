package dynamo_test

// Rudolph accepts only canonical UUIDs as machine IDs, and the view builds links and form actions from them
// (/machines/<id>, /admin/machines/<id>/config). A row with any other machine ID was not written by Rudolph's API;
// an ID of ".." would make the machine page's forms post to /admin/config, the global settings (stored-XSS review,
// 2026-11-18). The decoders refuse such rows, whether the ID comes from the MachineID attribute or the partition
// key, so lists skip them (as they already skip undecodable events) and a machine page answers not found.

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
)

var badMachineIDs = []string{"..", ".", "../config", "javascript:alert(1)", "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA/../x",
	"{AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA}", "AAAAAAAA11114111811 1AAAAAAAAAAAA", "AAAAAAAA-1111-4111-8111-AAAAAAAAAAA\u202e"}

func str(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func TestDecodeMachineListItem_RefusesANonUUIDMachineID(t *testing.T) {
	for _, id := range badMachineIDs {
		it := clone(row(t, "Machine#"+machineA, "Current"))
		it["MachineID"] = str(id)
		if m, err := dynamo.DecodeMachineListItem(it); err == nil {
			t.Errorf("MachineID %q decoded as %+v, want an error", id, m)
		}
	}
	// Either case is a valid UUID: Rudolph keeps the case a Mac syncs under.
	it := clone(row(t, "Machine#"+machineA, "Current"))
	it["MachineID"] = str(strings.ToLower(machineA))
	if _, err := dynamo.DecodeMachineListItem(it); err != nil {
		t.Errorf("lower-case UUID: %v", err)
	}
}

func TestDecodeMachinePartition_RefusesANonUUIDMachineID(t *testing.T) {
	for _, id := range badMachineIDs {
		rows := rowsWithPK(t, "Machine#"+machineA)
		for i, it := range rows {
			if sk, _ := it["SK"].(*types.AttributeValueMemberS); sk != nil && sk.Value == "Current" {
				c := clone(it)
				c["MachineID"] = str(id)
				rows[i] = c
			}
		}
		d, err := dynamo.DecodeMachinePartition(rows)
		if err == nil {
			t.Errorf("MachineID %q decoded as %+v, want an error", id, d.Machine)
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Logf("MachineID %q: %v", id, err) // any error is fine; not found is what the page shows
		}
	}
}

func eventItem(prefix, pk string, machineID *string) map[string]types.AttributeValue {
	it := map[string]types.AttributeValue{
		"PK":       str(prefix + pk),
		"SK":       str("00000001761989415000000#0001"),
		"TS":       str("1761989415000000"),
		"Decision": str("BLOCK_UNKNOWN"),
	}
	if machineID != nil {
		it["MachineID"] = str(*machineID)
	}
	return it
}

func TestDecodeEvents_RefuseANonUUIDMachineID(t *testing.T) {
	for _, id := range badMachineIDs {
		attr := id
		// The MachineID attribute, with a valid partition key.
		if e, err := dynamo.DecodeExecutionEvent(eventItem("Exec#", machineA, &attr)); err == nil {
			t.Errorf("execution event MachineID %q decoded as %+v, want an error", id, e)
		}
		if e, err := dynamo.DecodeFileAccessEvent(eventItem("FileAccess#", machineA, &attr)); err == nil {
			t.Errorf("file access event MachineID %q decoded as %+v, want an error", id, e)
		}
		// No MachineID attribute: the ID comes from the partition key.
		if e, err := dynamo.DecodeExecutionEvent(eventItem("Exec#", id, nil)); err == nil {
			t.Errorf("execution event partition %q decoded as %+v, want an error", id, e)
		}
		if e, err := dynamo.DecodeFileAccessEvent(eventItem("FileAccess#", id, nil)); err == nil {
			t.Errorf("file access event partition %q decoded as %+v, want an error", id, e)
		}
	}
	ok := machineA
	if _, err := dynamo.DecodeExecutionEvent(eventItem("Exec#", machineA, &ok)); err != nil {
		t.Errorf("a valid execution event: %v", err)
	}
	if _, err := dynamo.DecodeFileAccessEvent(eventItem("FileAccess#", machineA, nil)); err != nil {
		t.Errorf("a valid file access event: %v", err)
	}
}
