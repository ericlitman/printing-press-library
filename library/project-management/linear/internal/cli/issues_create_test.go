package cli

import (
	"reflect"
	"testing"
)

// TestIssueInputFromFlags pins the flag-to-IssueCreateInput mapping that both
// the dry-run and live paths of `issues create` depend on, and that
// `import issues` mirrors at the record level. It is the network-free
// characterization net for the createIssueFromInput refactor.
func TestIssueInputFromFlags(t *testing.T) {
	tests := []struct {
		name string
		in   func() map[string]any
		want map[string]any
	}{
		{
			name: "minimal title and team only",
			in: func() map[string]any {
				return issueInputFromFlags("Fix login", "ENG", "", "", "", "", 0, nil)
			},
			want: map[string]any{"title": "Fix login", "teamId": "ENG"},
		},
		{
			name: "all optional fields populated",
			in: func() map[string]any {
				return issueInputFromFlags("Big feature", "team-uuid", "desc body", "assignee-uuid", "project-uuid", "state-uuid", 2, []string{"label-a", "label-b"})
			},
			want: map[string]any{
				"title":      "Big feature",
				"teamId":     "team-uuid",
				"description": "desc body",
				"assigneeId": "assignee-uuid",
				"projectId":  "project-uuid",
				"stateId":    "state-uuid",
				"priority":   2,
				"labelIds":   []string{"label-a", "label-b"},
			},
		},
		{
			name: "priority zero is omitted (None)",
			in: func() map[string]any {
				return issueInputFromFlags("x", "ENG", "", "", "", "", 0, nil)
			},
			want: map[string]any{"title": "x", "teamId": "ENG"},
		},
		{
			name: "empty optional strings are omitted",
			in: func() map[string]any {
				return issueInputFromFlags("x", "ENG", "", "", "", "", 3, nil)
			},
			want: map[string]any{"title": "x", "teamId": "ENG", "priority": 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("issueInputFromFlags mismatch\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}
