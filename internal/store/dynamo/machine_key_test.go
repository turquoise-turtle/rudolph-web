package dynamo_test

import (
	"testing"

	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
)

// A valid MachineID attribute does not vouch for the row's partition key: the lists page through the key, so it is
// checked as well.
func TestDecoders_RefuseANonUUIDPartitionKeyBesideAValidAttribute(t *testing.T) {
	for _, id := range badMachineIDs {
		it := clone(row(t, "Machine#"+machineA, "Current"))
		it["PK"] = str("Machine#" + id)
		if m, err := dynamo.DecodeMachineListItem(it); err == nil {
			t.Errorf("machine list key %q decoded as %+v", id, m)
		}
		ok := machineA
		if e, err := dynamo.DecodeExecutionEvent(eventItem("Exec#", id, &ok)); err == nil {
			t.Errorf("execution event key %q decoded as %+v", id, e)
		}
		if e, err := dynamo.DecodeFileAccessEvent(eventItem("FileAccess#", id, &ok)); err == nil {
			t.Errorf("file access event key %q decoded as %+v", id, e)
		}
	}
}
