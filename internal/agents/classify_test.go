package agents

import (
	"strings"
	"testing"

	"github.com/gliedabrennung/halyk-agent/internal/domain"
)

func TestParseClassifyAcceptsFencedJSON(t *testing.T) {
	raw := "```json\n{\"labels\":[" +
		`{"i":1,"category":"utilities","contra":true,"confidence":0.9,"rationale":"refund of a power bill"},` +
		`{"i":0,"category":"revenue","contra":false,"confidence":0.95,"rationale":"customer settlement"}` +
		"]}\n```"
	out, err := parseClassify(raw, 2)
	if err != nil {
		t.Fatalf("parseClassify: %v", err)
	}
	if out[0].Category != domain.CatRevenue || out[0].Contra {
		t.Errorf("item 0 = %+v", out[0])
	}
	if out[1].Category != domain.CatUtilities || !out[1].Contra {
		t.Errorf("item 1 = %+v", out[1])
	}
}

func TestParseClassifyRejectsMissingItem(t *testing.T) {
	raw := `{"labels":[{"i":0,"category":"revenue","confidence":0.9}]}`
	_, err := parseClassify(raw, 2)
	if err == nil {
		t.Fatal("a missing item must be an error")
	}
	if !strings.Contains(err.Error(), "item 1") {
		t.Errorf("the error should name the missing item, got %v", err)
	}
}

func TestParseClassifyRejectsInventedCategory(t *testing.T) {
	raw := `{"labels":[{"i":0,"category":"cost_of_sales","confidence":0.9}]}`
	if _, err := parseClassify(raw, 1); err == nil {
		t.Fatal("a category outside the taxonomy must be rejected")
	}
}

func TestParseClassifyRejectsIndexOutsideBatch(t *testing.T) {
	raw := `{"labels":[{"i":7,"category":"revenue","confidence":0.9}]}`
	if _, err := parseClassify(raw, 1); err == nil {
		t.Fatal("an index outside the batch must be rejected")
	}
}

func TestTaxonomyBlockDescribesEveryCategory(t *testing.T) {
	block := taxonomyBlock()
	for _, c := range domain.Categories {
		line := ""
		for _, l := range strings.Split(block, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), string(c)+" ") {
				line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), string(c)))
				break
			}
		}
		if line == "" {
			t.Errorf("category %q has no description in the prompt", c)
		}
	}
}
