//go:build integration

package dynamo_test

// Configuration pages (step S1 of the admin plan): the store reads the global config (one GetItem of PK=GlobalConfig,
// SK=Config) and a Mac's own config (the SK=Config row of the GetMachine query it already makes) as store.Config.
// A setting is set (non-nil, or a non-empty ClientMode/OverrideFileAccessAction) only when the row stores it with a
// readable value, as Rudolph's `config get` marks (machine) only for settings the row stores; a mistyped value is left
// out, as elsewhere in decode.go. Exists says whether the row is there; without a global row Rudolph sends its
// defaults (MONITOR), so GlobalConfig returns ClientMode MONITOR with Exists false. USB settings are not read.

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/ddblocal"
)

func putConfigRow(t *testing.T, pk string, attrs map[string]types.AttributeValue) {
	t.Helper()
	item := map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: "Config"},
	}
	for k, v := range attrs {
		item[k] = v
	}
	if _, err := ddblocal.Raw(t).PutItem(ctx, &awsdynamodb.PutItemInput{TableName: aws.String(ddblocal.TableName), Item: item}); err != nil {
		t.Fatal(err)
	}
}

func cfgN(v string) types.AttributeValue  { return &types.AttributeValueMemberN{Value: v} }
func cfgS(v string) types.AttributeValue  { return &types.AttributeValueMemberS{Value: v} }
func cfgBool(v bool) types.AttributeValue { return &types.AttributeValueMemberBOOL{Value: v} }

func ptr[T any](v T) *T { return &v }

// configDiff names the fields of got that differ from want, with pointers compared by value.
func configDiff(got, want store.Config) string {
	g, w := reflect.ValueOf(got), reflect.ValueOf(want)
	var diffs []string
	for i := 0; i < g.NumField(); i++ {
		gv, wv := g.Field(i), w.Field(i)
		if gv.Kind() == reflect.Pointer {
			if gv.IsNil() != wv.IsNil() || (!gv.IsNil() && !reflect.DeepEqual(gv.Elem().Interface(), wv.Elem().Interface())) {
				diffs = append(diffs, fmt.Sprintf("%s: got %s, want %s", g.Type().Field(i).Name, show(gv), show(wv)))
			}
			continue
		}
		if !reflect.DeepEqual(gv.Interface(), wv.Interface()) {
			diffs = append(diffs, fmt.Sprintf("%s: got %v, want %v", g.Type().Field(i).Name, gv.Interface(), wv.Interface()))
		}
	}
	return strings.Join(diffs, "; ")
}

func show(v reflect.Value) string {
	if v.IsNil() {
		return "unset"
	}
	return fmt.Sprintf("%v", v.Elem().Interface())
}

func TestGlobalConfig_ReadsEverySetting(t *testing.T) {
	st := newStore(t)
	putConfigRow(t, "GlobalConfig", map[string]types.AttributeValue{
		"ClientMode": cfgN("2"), "BatchSize": cfgN("100"), "FullSyncInterval": cfgN("900"),
		"EnableBundles": cfgBool(true), "EnableTransitiveRules": cfgBool(false),
		"AllowedPathRegex": cfgS("^/opt/"), "BlockedPathRegex": cfgS("^/tmp/"),
		"UploadLogsUrl": cfgS("https://logs.example.com/u"), "EnableAllEventUpload": cfgBool(true),
		"DisableUnknownEventUpload": cfgBool(false), "OverrideFileAccessAction": cfgS("AUDIT_ONLY"),
		"EventDetailURL": cfgS("https://help.example.com/%file_sha%"), "EventDetailText": cfgS("Get help"),
		"BlockUsbMount": cfgBool(true),
	})
	got, err := st.GlobalConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := store.Config{
		Exists: true, ClientMode: store.ClientModeLockdown, BatchSize: ptr(100), FullSyncInterval: ptr(900),
		EnableBundles: ptr(true), EnableTransitiveRules: ptr(false), AllowedPathRegex: ptr("^/opt/"),
		BlockedPathRegex: ptr("^/tmp/"), UploadLogsURL: ptr("https://logs.example.com/u"),
		EnableAllEventUpload: ptr(true), DisableUnknownEventUpload: ptr(false), OverrideFileAccessAction: "AUDIT_ONLY",
		EventDetailURL: ptr("https://help.example.com/%file_sha%"), EventDetailText: ptr("Get help"),
	}
	if d := configDiff(got, want); d != "" {
		t.Errorf("global config differs: %s", d)
	}
}

func TestGlobalConfig_NoRowIsDefaults(t *testing.T) {
	st := newStore(t)
	if _, err := ddblocal.Raw(t).DeleteItem(ctx, &awsdynamodb.DeleteItemInput{
		TableName: aws.String(ddblocal.TableName),
		Key:       map[string]types.AttributeValue{"PK": cfgS("GlobalConfig"), "SK": cfgS("Config")},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GlobalConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Exists || got.ClientMode != store.ClientModeMonitor || got.BatchSize != nil {
		t.Errorf("no global row: %+v; want Exists false, MONITOR and nothing else set", got)
	}
}

func TestGlobalConfig_MistypedValuesAreLeftOut(t *testing.T) {
	st := newStore(t)
	putConfigRow(t, "GlobalConfig", map[string]types.AttributeValue{
		"ClientMode": cfgN("1"), "BatchSize": cfgS("lots"), "EnableBundles": cfgS("yes"), "FullSyncInterval": cfgN("600"),
	})
	got, err := st.GlobalConfig(ctx)
	if err != nil {
		t.Fatalf("a mistyped value failed the read: %v", err)
	}
	if got.BatchSize != nil || got.EnableBundles != nil || got.FullSyncInterval == nil || *got.FullSyncInterval != 600 {
		t.Errorf("got %+v; want the mistyped BatchSize and EnableBundles left out and FullSyncInterval 600", got)
	}
}

func TestGetMachine_ReadsTheMachinesOwnConfig(t *testing.T) {
	st := newStore(t)
	putConfigRow(t, "Machine#"+machineA, map[string]types.AttributeValue{
		"BatchSize": cfgN("25"), "DisableUnknownEventUpload": cfgBool(true), "ClientMode": cfgN("2"),
	})
	ddblocal.ResetCounts()
	m, err := st.GetMachine(ctx, machineA)
	if err != nil {
		t.Fatal(err)
	}
	if c := ddblocal.Counts(); c["Query"] != 1 || c["GetItem"] != 0 {
		t.Errorf("GetMachine made %v calls; want the one Query it made before", c)
	}
	want := store.Config{Exists: true, ClientMode: store.ClientModeLockdown, BatchSize: ptr(25), DisableUnknownEventUpload: ptr(true)}
	if d := configDiff(m.Config, want); d != "" {
		t.Errorf("machine config differs: %s", d)
	}
	if m.ConfiguredClientMode != store.ClientModeLockdown {
		t.Errorf("ConfiguredClientMode %q, want LOCKDOWN as before", m.ConfiguredClientMode)
	}
}
