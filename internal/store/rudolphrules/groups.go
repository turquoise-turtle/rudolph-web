package rudolphrules

import (
	"fmt"
	"strings"

	"github.com/airbnb/rudolph/pkg/model/groups"

	"github.com/turquoise-turtle/rudolph-web/internal/store"
)

// MaxGroups is how many groups Rudolph registers, the reserved group of Macs
// in no group not included.
const MaxGroups = groups.MaxGroups

// GroupName normalises the name of a group to create or assign as Rudolph's
// groups.Normalise does (Unicode NFC, trimmed, lower case), refusing what it
// refuses: empty, "." or "..", control characters, over 128 bytes, or the
// reserved "rudolph:" prefix.
func GroupName(name string) (string, error) {
	if isUngrouped(name) {
		return "", store.Invalid("the group of Macs in no group is Rudolph's own: a Mac is in it while it is in no other group")
	}
	n, err := groups.Normalise(name)
	if err != nil {
		return "", store.Invalid("%v", err)
	}
	return n, nil
}

// ExistingGroupName normalises the name of a registered group to update or
// delete, as the CLI's groups.NormaliseTarget does, but refuses the group of
// Macs in no group, which has no settings and is never deleted from here.
func ExistingGroupName(name, doing string) (string, error) {
	if isUngrouped(name) {
		return "", store.Invalid("the group of Macs in no group cannot be %s", doing)
	}
	return GroupName(name)
}

func isUngrouped(name string) bool {
	return strings.ToLower(strings.TrimSpace(name)) == groups.Ungrouped
}

// GroupExists is the refusal to create a group that is already registered.
func GroupExists(name string) error {
	return store.Invalid("there is already a group %q", name)
}

// TooManyGroups is the refusal to create a group beyond Rudolph's limit.
func TooManyGroups() error {
	return store.Invalid("at most %d groups can be registered; delete one first", MaxGroups)
}

// GroupHasRules is the refusal to delete a group with rules without force.
func GroupHasRules(name string, rules int) error {
	return store.Invalid("group %q has %d rules; remove them first, or tick \"also delete its rules\" to delete them with the group", name, rules)
}

// NotRegistered is the refusal to assign a group that is not registered.
func NotRegistered(name string) error {
	return store.Invalid("there is no group %q; create it first", name)
}

// GroupChanges keeps the fields of an update that differ from the group as
// it is; both nil means nothing would change.
func GroupChanges(current store.Group, description *string, acceptReported *bool) (*string, *bool) {
	if description != nil && *description == current.Description {
		description = nil
	}
	if acceptReported != nil && *acceptReported == current.AcceptReported {
		acceptReported = nil
	}
	return description, acceptReported
}

// reportedText says whether Macs that report a group are members, in the
// CLI's words.
func reportedText(accept bool) string {
	if accept {
		return "count as members"
	}
	return "are ignored (only assigned Macs are members)"
}

// CreateGroupSummary is the CLI's audit summary of group create.
func CreateGroupSummary(name, description string, acceptReported bool) string {
	return fmt.Sprintf("created group %s, description %q, Macs that report it %s", name, description, reportedText(acceptReported))
}

// UpdateGroupSummary is the CLI's audit summary of group update, naming the
// fields that are not nil.
func UpdateGroupSummary(name string, description *string, acceptReported *bool) string {
	var changes []string
	if description != nil {
		changes = append(changes, fmt.Sprintf("description %q", *description))
	}
	if acceptReported != nil {
		changes = append(changes, "Macs that report it "+reportedText(*acceptReported))
	}
	return fmt.Sprintf("updated group %s: %s", name, strings.Join(changes, ", "))
}

// DeleteGroupSummary is the CLI's audit summary of group delete; with force
// it says the rules went too, as the CLI does whether or not there were any.
func DeleteGroupSummary(name string, force bool) string {
	summary := "deleted group " + name
	if force {
		summary += " and its rules"
	}
	return summary
}

// PartialDeleteGroupSummary is the CLI's audit summary of a forced group
// delete that failed after deleting some of the group's rules.
func PartialDeleteGroupSummary(name, cause string) string {
	return fmt.Sprintf("deleting group %s and its rules (failed part-way: %s)", name, cause)
}

// AssignGroupSummary is the CLI's audit summary of group add.
func AssignGroupSummary(name string) string { return "assigned group " + name }

// UnassignGroupSummary is the CLI's audit summary of group remove.
func UnassignGroupSummary(name string) string { return "removed assigned group " + name }
