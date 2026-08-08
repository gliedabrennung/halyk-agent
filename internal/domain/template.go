package domain

import (
	"fmt"
	"strings"
)

type Cell struct {
	ScenarioID string `json:"scenario_id"`
	ClauseID   string `json:"clause_id"`
}

type Template struct {
	Team         string   `json:"team"`
	ContactEmail string   `json:"contact_email"`
	Model        string   `json:"model"`
	Scenarios    []string `json:"scenarios"`
	Cells        []Cell   `json:"cells"`
	Raw          []byte   `json:"-"`
}

func (t *Template) ScenariosFor(only []string) ([]string, error) {
	if len(only) == 0 {
		return t.Scenarios, nil
	}
	want := make(map[string]bool, len(only))
	for _, s := range only {
		want[s] = true
	}
	var out []string
	for _, s := range t.Scenarios {
		if want[s] {
			out = append(out, s)
			delete(want, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("none of the requested scenarios are in the template: %s",
			strings.Join(only, ", "))
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("not in the template: %s", strings.Join(SortedKeys(want), ", "))
	}
	return out, nil
}

func (t *Template) ClausesFor(scenarioID string) []string {
	var out []string
	for _, c := range t.Cells {
		if c.ScenarioID == scenarioID {
			out = append(out, c.ClauseID)
		}
	}
	return out
}
