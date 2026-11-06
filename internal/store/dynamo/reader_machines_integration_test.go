//go:build integration

package dynamo_test

// Step 6 of docs/data-layer-design.md: the machine list, from Rudolph's GSIs DataType_SerialNum (no filter, or a
// serial prefix) and DataType_HostnameKey (a hostname prefix). One Query per page, Limit = page size, never a Scan or
// a FilterExpression. The fixture Mac (serial C02AAAAAAAA1) is in the table too.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/dynamo"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

const (
	listMac1 = "11111111-1111-4111-8111-111111111111" // C02BBB000001, Build-01.Corp.Example
	listMac2 = "22222222-2222-4222-8222-222222222222" // FVF000000002, laptop-02.corp.example
	listMac3 = "33333333-3333-4333-8333-333333333333" // no serial, zz-noserial.corp.example
	listMac4 = "44444444-4444-4444-8444-444444444444" // C02CCC000003, no hostname
)

// putSensorData writes a sensor data row the way Rudolph does: SerialNum, Hostname and HostnameKey are left out when
// empty, HostnameKey is the trimmed lower-case hostname.
func putSensorData(t *testing.T, id, serial, hostname string, extra map[string]types.AttributeValue) {
	t.Helper()
	s := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	it := map[string]types.AttributeValue{
		"PK":              s("Machine#" + id),
		"SK":              s("Current"),
		"DataType":        s("SensorData"),
		"MachineID":       s(id),
		"ModelIdentifier": s("Mac15,7"),
		"OSVersion":       s("26.1"),
		"OSBuild":         s("25B78"),
		"SantaVersion":    s("2026.8"),
		"ClientMode":      n(1),
		"PrimaryUser":     s("alice"),
		"Time":            s("2026-11-05T09:00:00Z"),
		"ExpiresAfter":    n(int(now.Add(90 * 24 * time.Hour).Unix())),
	}
	if serial != "" {
		it["SerialNum"] = s(serial)
	}
	if hostname != "" {
		it["Hostname"] = s(hostname)
		it["HostnameKey"] = s(strings.ToLower(strings.TrimSpace(hostname)))
	}
	for k, v := range extra {
		it[k] = v
	}
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.TableName), Item: it}); err != nil {
		t.Fatal(err)
	}
}

func newListStore(t *testing.T) *dynamo.Store {
	t.Helper()
	s := newStore(t)
	putSensorData(t, listMac1, "C02BBB000001", "Build-01.Corp.Example", map[string]types.AttributeValue{
		"LastSeenDay": &types.AttributeValueMemberS{Value: "2026-11-04"},
		"ClientMode":  n(2),
		"PrimaryUser": &types.AttributeValueMemberS{Value: "bob"},
	})
	putSensorData(t, listMac2, "FVF000000002", "laptop-02.corp.example", nil)
	putSensorData(t, listMac3, "", "zz-noserial.corp.example", nil)
	putSensorData(t, listMac4, "C02CCC000003", "", nil)
	ddblocal.ResetCounts()
	return s
}

func ids(ms []store.Machine) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.MachineID)
	}
	return strings.Join(out, ",")
}

func listAll(t *testing.T, s *dynamo.Store, f store.MachineFilter, limit int) ([]store.Machine, int) {
	t.Helper()
	var got []store.Machine
	cursor := ""
	pages := 0
	for {
		page, err := s.ListMachines(ctx, f, store.PageRequest{Limit: limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages+1, err)
		}
		pages++
		got = append(got, page.Items...)
		if page.NextCursor == "" {
			return got, pages
		}
		if pages > 20 {
			t.Fatal("too many pages")
		}
		cursor = page.NextCursor
	}
}

func TestListMachines_AllOrderedBySerial(t *testing.T) {
	s := newListStore(t)
	limits := limitsUsed(t)
	got, pages := listAll(t, s, store.MachineFilter{}, 2)
	// A Mac that reports no serial is not in the serial index; it is found by hostname.
	if want := strings.Join([]string{machineA, listMac1, listMac4, listMac2}, ","); ids(got) != want {
		t.Errorf("order %s, want %s", ids(got), want)
	}
	queriesOnly(t, pages)
	for _, l := range *limits {
		if l != 2 {
			t.Errorf("Query Limit %d, want the page size 2 (limits %v)", l, *limits)
		}
	}
}

func TestListMachines_ItemFields(t *testing.T) {
	s := newListStore(t)
	page, err := s.ListMachines(ctx, store.MachineFilter{SerialPrefix: "C02BBB"}, store.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("got %s, want %s", ids(page.Items), listMac1)
	}
	m := page.Items[0]
	want := store.Machine{
		MachineID:       listMac1,
		SerialNum:       "C02BBB000001",
		Hostname:        "Build-01.Corp.Example",
		ModelIdentifier: "Mac15,7",
		OSVersion:       "26.1",
		SantaVersion:    "2026.8",
		ClientMode:      store.ClientModeLockdown,
		PrimaryUser:     "bob",
		// The list's last seen is LastSeenDay, a UTC date: midnight UTC of that day.
		LastSeen: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
	}
	if m.MachineID != want.MachineID || m.SerialNum != want.SerialNum || m.Hostname != want.Hostname ||
		m.ModelIdentifier != want.ModelIdentifier || m.OSVersion != want.OSVersion || m.SantaVersion != want.SantaVersion ||
		m.ClientMode != want.ClientMode || m.PrimaryUser != want.PrimaryUser || !m.LastSeen.Equal(want.LastSeen) {
		t.Errorf("got %+v\nwant %+v", m, want)
	}
}

// Rows written before Rudolph stored LastSeenDay, or with a value that is not a date, show no last seen rather than
// failing the list.
func TestListMachines_MissingOrOddLastSeenDay(t *testing.T) {
	s := newListStore(t)
	putSensorData(t, listMac2, "FVF000000002", "laptop-02.corp.example", map[string]types.AttributeValue{
		"LastSeenDay": &types.AttributeValueMemberS{Value: "yesterday"},
	})
	page, err := s.ListMachines(ctx, store.MachineFilter{SerialPrefix: "FVF"}, store.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || !page.Items[0].LastSeen.IsZero() {
		t.Errorf("odd LastSeenDay: %v %+v, want one Mac with a zero LastSeen", err, page.Items)
	}
	page, err = s.ListMachines(ctx, store.MachineFilter{SerialPrefix: "C02CCC"}, store.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || !page.Items[0].LastSeen.IsZero() {
		t.Errorf("no LastSeenDay: %v %+v, want one Mac with a zero LastSeen", err, page.Items)
	}
}

func TestListMachines_SerialPrefixIsTrimmedAndUpperCase(t *testing.T) {
	s := newListStore(t)
	got, _ := listAll(t, s, store.MachineFilter{SerialPrefix: "  c02 "}, 10)
	if want := strings.Join([]string{machineA, listMac1, listMac4}, ","); ids(got) != want {
		t.Errorf("serial prefix c02: %s, want %s", ids(got), want)
	}
	queriesOnly(t, 1)
}

func TestListMachines_HostnamePrefixIsCaseInsensitive(t *testing.T) {
	s := newListStore(t)
	for prefix, want := range map[string]string{
		" BUILD-":  listMac1,
		"zz":       listMac3,
		"Laptop":   listMac2,
		"mac-aaaa": machineA,
		"nothing":  "",
	} {
		ddblocal.ResetCounts()
		got, _ := listAll(t, s, store.MachineFilter{HostnamePrefix: prefix}, 10)
		if ids(got) != want {
			t.Errorf("hostname prefix %q: %s, want %s", prefix, ids(got), want)
		}
		queriesOnly(t, 1)
	}
	// Results from the hostname index show the serial as well.
	got, _ := listAll(t, s, store.MachineFilter{HostnamePrefix: "build-"}, 10)
	if len(got) != 1 || got[0].SerialNum != "C02BBB000001" || got[0].Hostname != "Build-01.Corp.Example" {
		t.Errorf("hostname search result %+v, want serial C02BBB000001 and hostname Build-01.Corp.Example", got)
	}
	// A Mac without a hostname is only in the serial list.
	got, _ = listAll(t, s, store.MachineFilter{HostnamePrefix: "c"}, 10)
	if strings.Contains(ids(got), listMac4) {
		t.Errorf("a Mac without a hostname was found by hostname: %s", ids(got))
	}
}

func TestListMachines_BlankPrefixMeansNoFilter(t *testing.T) {
	s := newListStore(t)
	got, _ := listAll(t, s, store.MachineFilter{SerialPrefix: "   "}, 10)
	if len(got) != 4 {
		t.Errorf("blank serial prefix: %s, want the 4 Macs with a serial", ids(got))
	}
}

func TestListMachines_InvalidArguments(t *testing.T) {
	s := newListStore(t)
	for name, c := range map[string]struct {
		f store.MachineFilter
		p store.PageRequest
	}{
		"both prefixes":  {store.MachineFilter{SerialPrefix: "C02", HostnamePrefix: "mac"}, store.PageRequest{Limit: 10}},
		"limit 0":        {store.MachineFilter{}, store.PageRequest{Limit: 0}},
		"limit over max": {store.MachineFilter{}, store.PageRequest{Limit: store.MaxPageSize + 1}},
	} {
		if _, err := s.ListMachines(ctx, c.f, c.p); !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: got %v, want store.ErrInvalidArgument", name, err)
		}
	}
	queriesOnly(t, 0)
}

func TestListMachines_CursorsAreBoundToTheFilter(t *testing.T) {
	s := newListStore(t)
	all, err := s.ListMachines(ctx, store.MachineFilter{}, store.PageRequest{Limit: 1})
	if err != nil || all.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, all)
	}
	serial, err := s.ListMachines(ctx, store.MachineFilter{SerialPrefix: "C02"}, store.PageRequest{Limit: 1})
	if err != nil || serial.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, serial)
	}
	rules, err := s.ListMachineRules(ctx, machineA, store.PageRequest{Limit: 1})
	if err != nil || rules.NextCursor == "" {
		t.Fatalf("precondition: %v %+v", err, rules)
	}
	for name, c := range map[string]struct {
		f      store.MachineFilter
		cursor string
	}{
		"unfiltered cursor with a serial prefix":   {store.MachineFilter{SerialPrefix: "C02"}, all.NextCursor},
		"serial cursor with another serial prefix": {store.MachineFilter{SerialPrefix: "C02B"}, serial.NextCursor},
		"serial cursor with a hostname prefix":     {store.MachineFilter{HostnamePrefix: "c02"}, serial.NextCursor},
		"machine rules cursor":                     {store.MachineFilter{}, rules.NextCursor},
		"garbage":                                  {store.MachineFilter{}, "garbage"},
	} {
		if _, err := s.ListMachines(ctx, c.f, store.PageRequest{Limit: 1, Cursor: c.cursor}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s: got %v, want store.ErrInvalidCursor", name, err)
		}
	}
	// The same prefix spelt differently is the same filter.
	if _, err := s.ListMachines(ctx, store.MachineFilter{SerialPrefix: " c02"}, store.PageRequest{Limit: 2, Cursor: serial.NextCursor}); err != nil {
		t.Errorf("same normalised filter: %v", err)
	}
}

func TestListMachines_UndecodableRowIsAnError(t *testing.T) {
	s := newListStore(t)
	putSensorData(t, listMac2, "FVF000000002", "laptop-02.corp.example", map[string]types.AttributeValue{
		"ClientMode": &types.AttributeValueMemberS{Value: "MONITOR"},
	})
	if _, err := s.ListMachines(ctx, store.MachineFilter{SerialPrefix: "FVF"}, store.PageRequest{Limit: 10}); err == nil ||
		errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrUnavailable) {
		t.Errorf("got %v, want a plain decode error", err)
	}
}
