package ingest

import (
	"strings"
	"testing"
)

const _sampleTemplate = `{
  "team": "",
  "contact_email": "",
  "model": "",
  "answers": {
    "P2": {
      "6.1": {"status": null, "actual": null, "evidence_txn_id": null},
      "6.3": {"status": null, "actual": null, "evidence_txn_id": null}
    },
    "P1": {
      "6.2": {"status": null, "actual": null, "evidence_txn_id": null}
    }
  }
}`

func TestParseTemplatePreservesOrder(t *testing.T) {
	tpl, err := parseTemplateBytes([]byte(_sampleTemplate))
	if err != nil {
		t.Fatalf("parseTemplateBytes: %v", err)
	}
	if got, want := strings.Join(tpl.Scenarios, ","), "P2,P1"; got != want {
		t.Errorf("scenarios = %q, want %q (document order)", got, want)
	}
	if got, want := len(tpl.Cells), 3; got != want {
		t.Fatalf("cells = %d, want %d", got, want)
	}
	want := []struct{ scn, clause string }{{"P2", "6.1"}, {"P2", "6.3"}, {"P1", "6.2"}}
	for i, w := range want {
		if tpl.Cells[i].ScenarioID != w.scn || tpl.Cells[i].ClauseID != w.clause {
			t.Errorf("cell[%d] = %s/%s, want %s/%s", i,
				tpl.Cells[i].ScenarioID, tpl.Cells[i].ClauseID, w.scn, w.clause)
		}
	}
	if got, want := strings.Join(tpl.ClausesFor("P2"), ","), "6.1,6.3"; got != want {
		t.Errorf("ClausesFor(P2) = %q, want %q", got, want)
	}
	if len(tpl.Raw) == 0 {
		t.Error("Raw must keep the original bytes for in-place filling")
	}
}

func TestParseTemplateRejectsBadShapes(t *testing.T) {
	tests := []struct {
		name, json, want string
	}{
		{
			name: "missing field",
			json: `{"answers":{"P1":{"6.1":{"status":null,"actual":null}}}}`,
			want: `missing field "evidence_txn_id"`,
		},
		{
			name: "extra field",
			json: `{"answers":{"P1":{"6.1":{"status":null,"actual":null,"evidence_txn_id":null,"note":1}}}}`,
			want: "unexpected fields [note]",
		},
		{
			name: "no answers",
			json: `{"team":"x"}`,
			want: `no "answers" object`,
		},
		{
			name: "empty answers",
			json: `{"answers":{}}`,
			want: "no answer cells",
		},
		{
			name: "not json",
			json: `{`,
			want: "parse template",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTemplateBytes([]byte(tt.json))
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestParseTemplateKeepsTopLevelFields(t *testing.T) {
	tpl, err := parseTemplateBytes([]byte(
		`{"team":"t","contact_email":"e@x.io","model":"m","answers":{"P1":{"6.1":{"status":null,"actual":null,"evidence_txn_id":null}}}}`))
	if err != nil {
		t.Fatalf("parseTemplateBytes: %v", err)
	}
	if tpl.Team != "t" || tpl.ContactEmail != "e@x.io" || tpl.Model != "m" {
		t.Errorf("top-level fields = %q/%q/%q", tpl.Team, tpl.ContactEmail, tpl.Model)
	}
}
