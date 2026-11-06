package web_test

// Copy-and-paste CLI commands on the pages (internal/clicmd): the environment name is a web.Options setting
// (RUDOLPH_WEB_CLI_ENV); each command sits in a <code> element with a copy button. Event rows and top-list entries
// offer block/allow by binary (global and, on events, for that Mac); the machine page offers config get, a temporary
// monitor mode for 2 hours and its end, and the machine's rule list; global and machine rule rows offer removal; a
// group page offers a group rule. Values of the wrong shape get no command.

import (
	"html"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/turquoise-turtle/rudolph-web/internal/auth"
	"github.com/turquoise-turtle/rudolph-web/internal/store"
	"github.com/turquoise-turtle/rudolph-web/internal/store/fake"
	"github.com/turquoise-turtle/rudolph-web/internal/testutil/fakeidp"
	"github.com/turquoise-turtle/rudolph-web/internal/web"
)

const cliMachine = "AAAAAAAA-1111-4111-8111-AAAAAAAAAAAA"

func newAppWithCLIEnv(t *testing.T, s *fake.Store, env string) *app {
	t.Helper()
	idp := fakeidp.New(t, tenantID, clientID, "secret")
	a, err := auth.New(auth.Config{
		Issuer: idp.Issuer, TenantID: tenantID, ClientID: clientID, ClientSecret: "secret",
		RedirectURL: origin + "/auth/callback", CookieKey: []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL: time.Hour, Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.New(web.Options{Auth: a, Store: s, PageSize: 10, Now: func() time.Time { return fixedNow }, CLIEnv: env})
	if err != nil {
		t.Fatal(err)
	}
	return &app{h: h, auth: a, store: s, idp: idp}
}

func mustContainCommand(t *testing.T, body, cmd string) {
	t.Helper()
	if !strings.Contains(body, html.EscapeString(cmd)) {
		t.Errorf("page lacks the command %s", cmd)
	}
}

func TestCommands_EventsPage(t *testing.T) {
	s := fake.New()
	good := strings.Repeat("0", 63) + "e"
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: cliMachine, FileSHA256: good, FileName: "tool", Decision: store.DecisionBlockUnknown, ExecutionTime: fixedNow})
	s.AddExecutionEvent(store.ExecutionEvent{MachineID: cliMachine, FileSHA256: "not-a-hash", FileName: "odd", Decision: store.DecisionBlockUnknown, ExecutionTime: fixedNow.Add(-time.Second)})
	a := newAppWithCLIEnv(t, s, "prod")
	code, body := a.viewerGet(t, "/events")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	mustContainCommand(t, body, "ENV=prod rudolph rule allow -t binary -i '"+good+"' --global --yes")
	mustContainCommand(t, body, "ENV=prod rudolph rule allow -t binary -i '"+good+"' --machine '"+cliMachine+"' --expires 24h --yes")
	mustContainCommand(t, body, "ENV=prod rudolph rule deny -t binary -i '"+good+"' --global --yes")
	if strings.Contains(body, "not-a-hash' ") || strings.Contains(body, "-i &#39;not-a-hash") {
		t.Error("a command was built for an identifier that is not a SHA-256")
	}
}

func TestCommands_MachinePage(t *testing.T) {
	s := fake.New()
	s.AddMachine(store.Machine{MachineID: cliMachine, Hostname: "mac", LastSeen: fixedNow})
	s.AddMachineRule(cliMachine, store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyAllowlist, Identifier: strings.Repeat("0", 63) + "b"})
	a := newAppWithCLIEnv(t, s, "prod")
	_, body := a.viewerGet(t, "/machines/"+cliMachine)
	mustContainCommand(t, body, "ENV=prod rudolph config get --machine '"+cliMachine+"'")
	mustContainCommand(t, body, "ENV=prod rudolph config update --machine '"+cliMachine+"' -c monitor --for 2h --yes")
	mustContainCommand(t, body, "ENV=prod rudolph rules --machine '"+cliMachine+"'")
	mustContainCommand(t, body, "ENV=prod rudolph rule remove -t binary -i '"+strings.Repeat("0", 63)+"b' --machine '"+cliMachine+"' --yes")
}

func TestCommands_RulesAndGroupPages(t *testing.T) {
	s := fake.New()
	s.AddGlobalRule(store.Rule{RuleType: store.RuleTypeBinary, Policy: store.PolicyBlocklist, Identifier: strings.Repeat("0", 63) + "1"})
	s.AddGroup(store.Group{Name: "eng", AcceptReported: true})
	s.AddGroupRule("eng", store.Rule{RuleType: store.RuleTypeSigningID, Policy: store.PolicyBlocklist, Identifier: "EQHXZ8M8AV:com.google.GeminiMacOS"})
	a := newAppWithCLIEnv(t, s, "prod")
	_, body := a.viewerGet(t, "/rules")
	mustContainCommand(t, body, "ENV=prod rudolph rule remove -t binary -i '"+strings.Repeat("0", 63)+"1' --global --yes")
	_, body = a.viewerGet(t, "/groups/eng")
	mustContainCommand(t, body, "ENV=prod rudolph rule deny -t signingid -i 'EQHXZ8M8AV:com.google.GeminiMacOS' --group 'eng' --yes")
}

func TestCommands_TopListOffersBlockAndAllow(t *testing.T) {
	s := fake.New()
	good := strings.Repeat("0", 63) + "e"
	s.SetTopList(store.TopKindAllowed, 7, fixedNow, []store.TopBinary{{FileSHA256: good, FileName: "tool", Count: 3}})
	a := newAppWithCLIEnv(t, s, "prod")
	_, body := a.viewerGet(t, "/events/top-allowed")
	mustContainCommand(t, body, "ENV=prod rudolph rule deny -t binary -i '"+good+"' --global --yes")
}
