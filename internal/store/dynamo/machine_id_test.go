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
	// The partition key carries the same spelling, as Rudolph writes both from one ID.
	it := clone(row(t, "Machine#"+machineA, "Current"))
	it["PK"], it["MachineID"] = str("Machine#"+strings.ToLower(machineA)), str(strings.ToLower(machineA))
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

// Rudolph writes a row's partition key and its MachineID attribute from the same ID, keeping the case the Mac sent
// (Rudolph's ResolveMachineID: IDs are matched exactly, case included). A row whose two IDs differ was not written by
// Rudolph, and the view would show one Mac's data with links and form actions built for the other, so the decoders
// refuse it as they refuse a non-UUID ID; only an exact match is the same Mac (path traversal review, 2026-11-19).
var otherMachineIDs = []string{machineB, strings.ToLower(machineA)}

func TestDecodeMachineListItem_RefusesAMachineIDOtherThanThePartitions(t *testing.T) {
	for _, id := range otherMachineIDs {
		it := clone(row(t, "Machine#"+machineA, "Current"))
		it["MachineID"] = str(id)
		if m, err := dynamo.DecodeMachineListItem(it); err == nil {
			t.Errorf("PK Machine#%s with MachineID %q decoded as %+v, want an error", machineA, id, m)
		}
	}
}

func TestDecodeMachinePartition_RefusesAMachineIDOtherThanThePartitions(t *testing.T) {
	for _, id := range otherMachineIDs {
		rows := rowsWithPK(t, "Machine#"+machineA)
		for i, it := range rows {
			if sk, _ := it["SK"].(*types.AttributeValueMemberS); sk != nil && sk.Value == "Current" {
				c := clone(it)
				c["MachineID"] = str(id)
				rows[i] = c
			}
		}
		if d, err := dynamo.DecodeMachinePartition(rows); err == nil {
			t.Errorf("partition Machine#%s with MachineID %q decoded as %+v, want an error", machineA, id, d.Machine)
		}
	}
}

func TestDecodeEvents_RefuseAMachineIDOtherThanThePartitions(t *testing.T) {
	for _, id := range otherMachineIDs {
		attr := id
		if e, err := dynamo.DecodeExecutionEvent(eventItem("Exec#", machineA, &attr)); err == nil {
			t.Errorf("PK Exec#%s with MachineID %q decoded as %+v, want an error", machineA, id, e)
		}
		if e, err := dynamo.DecodeFileAccessEvent(eventItem("FileAccess#", machineA, &attr)); err == nil {
			t.Errorf("PK FileAccess#%s with MachineID %q decoded as %+v, want an error", machineA, id, e)
		}
	}
}

// A Mac that syncs under a lower-case ID has it in both places, and is shown.
func TestDecoders_AcceptTheSameLowerCaseMachineIDInBoth(t *testing.T) {
	lower := strings.ToLower(machineA)
	it := clone(row(t, "Machine#"+machineA, "Current"))
	it["PK"], it["MachineID"] = str("Machine#"+lower), str(lower)
	if m, err := dynamo.DecodeMachineListItem(it); err != nil || m.MachineID != lower {
		t.Errorf("machine list item: %+v %v, want %s", m, err, lower)
	}
	var rows []item
	for _, r := range rowsWithPK(t, "Machine#"+machineA) {
		c := clone(r)
		c["PK"] = str("Machine#" + lower)
		if _, ok := c["MachineID"]; ok {
			c["MachineID"] = str(lower)
		}
		rows = append(rows, c)
	}
	if d, err := dynamo.DecodeMachinePartition(rows); err != nil || d.Machine.MachineID != lower {
		t.Errorf("machine partition: %+v %v, want %s", d.Machine, err, lower)
	}
	if e, err := dynamo.DecodeExecutionEvent(eventItem("Exec#", lower, &lower)); err != nil || e.MachineID != lower {
		t.Errorf("execution event: %+v %v, want %s", e, err, lower)
	}
	if e, err := dynamo.DecodeFileAccessEvent(eventItem("FileAccess#", lower, &lower)); err != nil || e.MachineID != lower {
		t.Errorf("file access event: %+v %v, want %s", e, err, lower)
	}
}
