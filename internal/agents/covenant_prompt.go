package agents

import (
	"fmt"
	"strings"
)

func covenantInstruction() string {
	return `You convert one clause of a credit agreement into an executable specification.

You do NOT decide compliance and you do NOT compute anything. You describe what must be
computed, precisely enough that a program can do it without reading the contract.

Return STRICT JSON with exactly these keys:

{
  "clause_id": "the number of the clause you were given, e.g. 7.4",
  "title": "short title of the covenant, from the clause heading",
  "expression": "arithmetic over term names, e.g. capex / (opex + rent)",
  "terms": [
    {
      "name": "capex",
      "kind": "statement_line | statement_note | ledger_category | related_party_payments | group_consolidated | constant",
      "line": "the line item or category exactly as the clause names it",
      "description": "the clause's own definition of this quantity, quoted or closely paraphrased",
      "reclassification": "include_in | exclude_from | both | ignore",
      "entity_source": "kyc | corporate_structure | compliance_file | ias24 | \"\"",
      "entity_scope": "restricted | unrestricted | \"\"",
      "category": "one of the taxonomy below, or \"\" — see rule 3b",
      "direction": "outflow | inflow | any",
      "constant": "only for kind=constant, a decimal string"
    }
  ],
  "op": "<= | >= | < | >",
  "threshold": "decimal string, no currency sign, no thousands separators",
  "unit": "USD | ratio",
  "period": {"kind": "fiscal_year | quarter | trailing_12m | point_in_time", "from": "YYYY-MM-DD", "to": "YYYY-MM-DD", "label": ""},
  "trigger": null,
  "carveouts": [],
  "evidence_kind": "single_txn | aggregate | ratio",
  "quote": "the sentence that states the threshold, verbatim",
  "confidence": 0.0
}

Rules that decide whether the specification is usable:

0. TERM NAMES. Use short lower-case ASCII identifiers: revenue, opex, capex, rent,
   payroll, utilities, taxes, interest_expense, ebitda, related_party_payments. Never use
   Cyrillic or spaces in a term name; the line item's Russian wording goes in "line".

1. EXPRESSION. Use only term names, numbers, + - * / parentheses, and the functions
   max(a, b) and min(a, b). The expression must produce the quantity the clause limits —
   the thing whose value would be compared with the threshold. Never fold the threshold
   into the expression.
   - "no single overhead line may exceed X, checked on the larger of payroll and utilities"
     is expression "max(payroll, utilities)", NOT "payroll + utilities".
   - "revenue less the larger of payroll and taxes" is "revenue - max(payroll, taxes)".
   - A ratio "A to B of at least 1.45x" is expression "A / B" with op ">=" and unit "ratio".

2. DIRECTION OF THE COMPARISON. "не превышал X" / "shall not exceed X" is op "<=".
   "не менее X" / "at least X" is op ">=". "не допускать снижения ниже X" is op ">=".
   Read the sentence, do not guess from the covenant's name.

3. TERM KIND.
   - "statement_line": a line of the audited financial statements ("Выручка",
     "Капитальные затраты", "Операционные расходы", "Процентные расходы", ...).
   - "statement_note": a figure disclosed only in the notes (e.g. a severance or
     retention programme liability).
   - "related_party_payments": payments to related or affiliated parties. Set
     entity_source to where the clause says that status is established.
   - "group_consolidated": a figure from the parent's consolidated statements, not the
     borrower's own.
   - "ledger_category": use only when the clause points at the borrower's own
     transaction records rather than the audited statements.
   - "constant": a literal used inside the expression.

3a. ENTITY SCOPE. A clause may narrow a quantity to counterparties of one security status:
   only subsidiaries outside the security perimeter ("unrestricted"), or only those inside it
   ("restricted"). Set "entity_scope" when the clause names such a status, and leave it empty
   otherwise — empty means every counterparty counts. This is NOT the related-party test: a
   clause capping payments to related or affiliated parties is kind=related_party_payments with
   entity_scope empty, even when the clause heading itself uses the word "restricted" as the
   defined name of those payments. It is equally meaningless for a line of the audited
   statements — revenue, operating costs, capital expenditure are figures, not counterparties.
   Leave it empty unless the clause itself limits the term to subsidiaries of one status.

3b. CATEGORY. Name the category of the borrower's own records this term sums, from the taxonomy
   at the end of these rules. It is what makes the term computable: the engine adds up the ledger
   rows carrying that category, so a term whose category names a different bucket than the rows
   sums the wrong rows. Read the clause's own wording for the quantity and pick the closest leaf;
   when none of them covers it, take the residual one for its side rather than inventing a name.
   Leave it empty for kind=constant, kind=group_consolidated, kind=related_party_payments and
   kind=statement_note — those are not sums over a category.

4. RECLASSIFICATION. State what the clause says about amounts the auditor reallocated:
   "include_in" when amounts moved INTO the line count; "exclude_from" when amounts moved
   OUT are dropped; "both" when the auditor's allocation governs in both directions;
   "ignore" when the clause does not mention it. Reclassifications that the auditors
   considered and REJECTED never count — if the clause says so, put it in the term
   description.

5. TRIGGER. A covenant that applies only under a condition ("применяется только при
   условии, что ...", "springing") gets a trigger:
   {"expression": "financing_receipts > 12500000", "description": "...", "source_quote": "..."}
   Any term the trigger uses must also appear in "terms". A covenant with no such
   condition has trigger null. A threshold is not a trigger.

6. CARVE-OUT. An exception under which exceeding the threshold is still permitted goes in
   "carveouts": [{"condition": {"expression": "...", "description": "...", "source_quote": "..."},
   "description": "...", "cap": "decimal string or empty"}]. Lender consent in writing is a
   carve-out. An empty list is correct when there is none.
   "condition.expression" is arithmetic over declared term names, exactly like "expression":
   every name in it MUST also appear in "terms". Most carve-outs cannot be measured that way —
   lender consent, a waiver letter, a regulator's approval are facts about paperwork, not sums
   in the ledger. For those leave "expression" empty and put the exception in "description".
   Never invent a term name such as "lender_consent" to stand for something the ledger cannot
   measure: the specification is rejected as a whole when a name is not a real term.

7. PERIOD. "from" and "to" are REQUIRED and must be real dates — the engine selects
   transactions with them, and an empty period selects nothing. Take them from the clause.
   A clause naming the fourth fiscal quarter of a year ending 2019-12-31 is kind "quarter",
   from 2019-10-01 to 2019-12-31. For kind "point_in_time" use the measurement window the
   clause gives for its components and let "to" be the as-of date: a liability measured as at
   2019-12-31 that includes costs incurred over the year to that date is from 2019-01-01 to
   2019-12-31. The dates here are worked examples — read the real ones off this clause.

7a. VOCABULARY. "reclassification" is exactly one of include_in, exclude_from, both, ignore.
   "entity_source" is exactly one of kyc, corporate_structure, compliance_file, ias24, or the
   empty string — it says where RELATED-PARTY status is established and is meaningless for
   any other kind of term. It is never a company name.

8. EVIDENCE KIND. "ratio" for any ratio; "aggregate" for a sum over a category or over
   related parties; "single_txn" only when the clause limits ONE transaction on its own
   ("ни одна операция не должна превышать").

9. THRESHOLD. "$2,750,000.00" is "2750000.00". "0.37x" is "0.37". "0.06x от выручки" is
   "0.06" with unit "ratio".
   These are formatting examples with invented numbers. Never carry one into your answer:
   the threshold is whatever THIS clause states.

Categories:

` + taxonomyBlock() + `
Output JSON only. No prose, no markdown fence.`
}

func (in CovenantInput) prompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Borrower: %s (scenario %s)\nClause to specify: %s\n", in.Company, in.ScenarioID, in.ClauseID)
	fmt.Fprintf(&b, "\n--- the clause ---\n%s\n", in.ClauseText)
	if in.AmendmentsIn != "" {
		fmt.Fprintf(&b, "\n--- amendment affecting this clause (it governs over the original) ---\n%s\n", in.AmendmentsIn)
	}
	if in.ArticleText != "" {
		fmt.Fprintf(&b, "\n--- the rest of the covenant article, for context ---\n%s\n", in.ArticleText)
	}
	if in.Definitions != "" {
		fmt.Fprintf(&b, "\n--- defined terms from the agreement ---\n%s\n", in.Definitions)
	}
	return b.String()
}
