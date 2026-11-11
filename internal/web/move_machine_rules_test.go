package web_test

// Moving a Mac's rules to another Mac (owner, 2026-11-11): when a user changes computers, their machine-specific
// rules go to the new Mac and come off the old one. Admins, editing on, on the old Mac's page:
//   - a form posting to /admin/machines/{id}/rules/move with `to` (the new Mac's serial number or machine ID,
//     trimmed, any case) and `remove` ("on" by default: also remove them from this Mac; unticked copies them);
//   - on success, 303 to the new Mac's page;
//   - every rule in force on the old Mac is copied as stored (policy, message, expiry, collection flag), each
//     recorded as the CLI's rule.add on the new Mac, then, with remove, each removed from the old Mac (rule.remove);
//   - refused with 400 and nothing written: the new Mac is the same Mac, no Mac matches `to`, the old Mac has no rules
//     in force, or the new Mac already has a rule for the same identifier with a different policy (the page names
//     it). A rule the new Mac already has with the same policy is left as it is, not copied again.
// Rules that are expired or being removed are not moved.

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
)

const (
	oldMac = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"
	newMac = "BBBBBBBB-2222-4222-8222-BBBBBBBBBBBB"
)

var (
	moveA = strings.Repeat("a1", 32)
	moveB = strings.Repeat("b2", 32)
	moveC = strings.Repeat("c3", 32)
)

func moveStore() *fake.Store {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: oldMac, SerialNum: "C02OLD", Hostname: "old-mac", LastSeen: fixedNow})
	s.AddMachine(store.Machine{MachineID: newMac, SerialNum: "C02NEW", Hostname: "new-mac", LastSeen: fixedNow})
	s.AddMachineRule(oldMac, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: moveA, State: store.RuleStateInForce, CustomMessage: "Lab tool"})
	s.AddMachineRule(oldMac, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: moveB, State: store.RuleStateInForce, ExpiresAt: fixedNow.Add(48 * time.Hour), CollectAllowedEvents: true})
	s.AddMachineRule(oldMac, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: moveC, State: store.RuleStateExpired, ExpiresAt: fixedNow.Add(-time.Hour)})
	return s
}

func machineRulesOf(t *testing.T, s *fake.Store, id string) map[string]store.Rule {
	t.Helper()
	p, err := s.ListMachineRules(context.Background(), id, store.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Rule{}
	for _, r := range p.Items {
		out[r.Identifier] = r
	}
	return out
}

func TestMoveRules_FormForAdminsOnly(t *testing.T) {
	s := moveStore()
	a := newEditApp(t, s, true)
	body := a.get(t, "/machines/"+oldMac, a.cookie(t, auth.RoleAdmin)).Body.String()
	mustContain(t, body, `action="/admin/machines/`+oldMac+`/rules/move"`, `name="to"`, `name="remove" value="on" checked`)
	_, body = a.viewerGet(t, "/machines/"+oldMac)
	mustNotContain(t, body, "/rules/move")
	if rec := a.post(t, "/admin/machines/"+oldMac+"/rules/move", url.Values{"to": {"C02NEW"}, "remove": {"on"}}, a.cookie(t, auth.RoleViewer), true); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status %d, want 403", rec.Code)
	}
	if len(machineRulesOf(t, s, newMac)) != 0 {
		t.Error("a viewer's POST moved rules")
	}
}

func TestMoveRules_BySerialMovesRulesInForce(t *testing.T) {
	s := moveStore()
	a := newEditApp(t, s, true)
	admin := a.cookie(t, auth.RoleAdmin)
	rec := a.post(t, "/admin/machines/"+oldMac+"/rules/move", url.Values{"to": {" c02new "}, "remove": {"on"}}, admin, true)
	see303(t, rec, "/machines/"+newMac)

	got := machineRulesOf(t, s, newMac)
	if len(got) != 2 {
		t.Fatalf("new Mac rules %+v; want the two in force", got)
	}
	if r := got[moveA]; r.Policy != store.PolicyAllowlist || r.CustomMessage != "Lab tool" || r.State != store.RuleStateInForce {
		t.Errorf("moved rule A %+v", r)
	}
	if r := got[moveB]; !r.ExpiresAt.Equal(fixedNow.Add(48*time.Hour)) || !r.CollectAllowedEvents {
		t.Errorf("moved rule B %+v; want its expiry and collection kept", r)
	}
	if _, ok := got[moveC]; ok {
		t.Error("the expired rule was moved")
	}
	for id, r := range machineRulesOf(t, s, oldMac) {
		if r.State == store.RuleStateInForce {
			t.Errorf("rule %s still in force on the old Mac", id[:8])
		}
	}

	var adds, removes int
	for _, e := range auditOf(t, s) {
		switch {
		case e.Action == "rule.add" && e.Target == "machine:"+newMac:
			adds++
		case e.Action == "rule.remove" && e.Target == "machine:"+oldMac:
			removes++
		default:
			t.Errorf("unexpected audit entry %+v", e)
		}
	}
	if adds != 2 || removes != 2 {
		t.Errorf("audit: %d adds on the new Mac and %d removes on the old; want 2 and 2", adds, removes)
	}
}

func TestMoveRules_CopyOnlyByMachineID(t *testing.T) {
	s := moveStore()
	a := newEditApp(t, s, true)
	rec := a.post(t, "/admin/machines/"+oldMac+"/rules/move", url.Values{"to": {strings.ToLower(newMac)}}, a.cookie(t, auth.RoleAdmin), true)
	see303(t, rec, "/machines/"+newMac)
	if len(machineRulesOf(t, s, newMac)) != 2 {
		t.Error("rules not copied")
	}
	if r := machineRulesOf(t, s, oldMac)[moveA]; r.State != store.RuleStateInForce {
		t.Errorf("copy only removed the old Mac's rule: %+v", r)
	}
}

func TestMoveRules_SamePolicyAlreadyThereIsKept(t *testing.T) {
	s := moveStore()
	s.AddMachineRule(newMac, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: moveA, State: store.RuleStateInForce})
	a := newEditApp(t, s, true)
	rec := a.post(t, "/admin/machines/"+oldMac+"/rules/move", url.Values{"to": {"C02NEW"}, "remove": {"on"}}, a.cookie(t, auth.RoleAdmin), true)
	see303(t, rec, "/machines/"+newMac)
	var adds int
	for _, e := range auditOf(t, s) {
		if e.Action == "rule.add" {
			adds++
		}
	}
	if adds != 1 {
		t.Errorf("%d rule.add entries, want 1 (rule A was already there)", adds)
	}
}

func TestMoveRules_Refusals(t *testing.T) {
	for name, c := range map[string]struct {
		setup func(*fake.Store)
		to    string
		want  string
	}{
		"same Mac":    {nil, "C02OLD", ""},
		"no such Mac": {nil, "C02NOPE", ""},
		"conflict": {func(s *fake.Store) {
			s.AddMachineRule(newMac, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: moveB, State: store.RuleStateInForce})
		}, "C02NEW", moveB},
		"nothing to move": {func(s *fake.Store) { s.AddMachine(store.Machine{MachineID: "DDDDDDDD-4444-4444-8444-DDDDDDDDDDDD", SerialNum: "C02EMPTY"}) }, "", ""},
	} {
		s := moveStore()
		if c.setup != nil {
			c.setup(s)
		}
		from, to := oldMac, c.to
		if name == "nothing to move" {
			from, to = "DDDDDDDD-4444-4444-8444-DDDDDDDDDDDD", "C02NEW"
		}
		before := len(machineRulesOf(t, s, newMac))
		a := newEditApp(t, s, true)
		rec := a.post(t, "/admin/machines/"+from+"/rules/move", url.Values{"to": {to}, "remove": {"on"}}, a.cookie(t, auth.RoleAdmin), true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
			continue
		}
		if c.want != "" {
			mustContain(t, rec.Body.String(), c.want)
		}
		if len(auditOf(t, s)) != 0 || len(machineRulesOf(t, s, newMac)) != before {
			t.Errorf("%s: something was written", name)
		}
	}
}
