package agents

import (
	"fmt"
	"strings"
)

const _factsInstruction = `You read a borrower's audit file and compliance dossier and extract, exactly, the
disclosures that change what a loan covenant sees.

You do NOT compute totals and you do NOT decide compliance. You transcribe disclosures.

Return STRICT JSON:

{
  "adjustments": [
    {
      "kind": "reclassify | exclude_period | include_period | disclosed_amount | ledger_amount_fix | ebitda_add_back | no_change",
      "txn_id": "TXN-P1-0045 if the disclosure names one, else empty",
      "counterparty": "the counterparty named, else empty",
      "amount": "decimal string, no currency sign or separators; empty if the disclosure states none",
      "from_category": "the category the amount was originally booked to, else empty",
      "to_category": "the category it is moved to, else empty",
      "rationale": "the auditor's stated reason, briefly",
      "applied": true|false,
      "source_doc": "the document id this came from",
      "quote": "the sentence, verbatim"
    }
  ],
  "parties": [
    {"name": "...", "voting_share": "23.4", "pledged_share": "87.6 if a collateral table gives this entity's pledged asset share, else empty",
     "relation": "affiliate|subsidiary|parent|", "status": "restricted|unrestricted| — only if stated in words", "source_doc": "...", "quote": "..."}
  ],
  "related_party_threshold": "the voting share percentage at or above which a counterparty counts as related, e.g. 25.0; empty if the file states none",
  "unrestricted_threshold": "the pledged-asset percentage BELOW which a subsidiary is outside the security and counts as unrestricted, e.g. 50.0; empty if the file states none",
  "fx_rates": [{"currency": "EUR", "usd_rate": "1.16", "basis": "how the rate was established", "quote": "..."}],
  "notes": ["anything material you saw that does not fit above"],
  "confidence": 0.0
}

Rules:

1. APPLIED vs REJECTED. "applied" is false when the auditor says the question was considered
   and the original classification is retained, or that no adjustment is required, or that the
   matter was reviewed and no change was made. Record those with kind "no_change" — they exist
   so a later stage can prove they were NOT applied. Everything the auditor actually concluded
   gets applied true.

2. ONLY FINAL POSITIONS. If a document says it is a draft, an interim worksheet, a preliminary
   position, or that it is replaced by a final report, IGNORE its conclusions entirely. Do not
   emit adjustments from it. Say so in "notes".

3. AN EMPTY NOTE MEANS ZERO, NOT UNKNOWN. If a heading such as "EBITDA adjustments" appears
   with no items under it, emit no adjustments for it and add a note saying the section was
   present and empty. Never invent an item to fill it.

4. THRESHOLD. The related-party threshold is stated in the compliance dossier ("Организации, в
   которых Группа владеет 25.0% и более голосующих прав, признаются связанными сторонами").
   It differs between borrowers. Take the number from THIS borrower's file. Do not assume.

5. PARTIES. Transcribe every organisation in the ownership table with its voting share, whether
   or not it meets the threshold — a share below the threshold is evidence too. Do not decide
   relatedness yourself; just report the shares.

5a. COLLATERAL COVERAGE. A dossier may carry a SECOND table ("Обеспечительное покрытие дочерних
   организаций") giving the share of each subsidiary's assets pledged under the security
   agreement, plus the percentage below which a subsidiary is outside the security perimeter and
   therefore "unrestricted". That table names entities the ownership table does not, and it is
   usually an OCR page. Transcribe those entities too, with "pledged_share" set and
   "voting_share" empty, and put the percentage in "unrestricted_threshold". Do not decide
   restricted status yourself.

6. AMOUNTS. Transcribe exactly as written: "$1,104,663.28" becomes "1104663.28".

6a. MISSING LEDGER AMOUNTS. The prompt names the rows whose amount the ledger export left blank.
   If ANY document in this file states the amount of such a row — a treasury memo, an internal
   note, a footnote — emit it as "ledger_amount_fix" with applied true and the txn_id. This is
   not a reclassification: an audit report saying that no reclassifications were required says
   nothing about a missing figure, and leaving it out reports the row as zero.

7. OCR PAGES. Some pages have no text layer and were transcribed by OCR; the ownership table is
   often one of them. The transcript can be noisy: read it for its structure, and prefer a figure
   the surrounding text confirms over a lone garbled one.

Output JSON only. No prose, no markdown fence.`

const _groupPPEInstruction = `
Add one more key to your JSON, "group_ppe", built from the CONSOLIDATED statements above:

{
  "parent": "the parent company the statements belong to",
  "period": "the year they cover, e.g. 2025",
  "opening": "net book value of property, plant and equipment at the START of the year",
  "closing": "net book value at the END of the year",
  "depreciation": "the depreciation charge for the year",
  "disposals": "net book value of disposals during the year; \"0\" if the note says there were none",
  "disposals_stated": true|false,
  "source_doc": "the document id",
  "quote": "the sentence about disposals, verbatim"
}

Transcribe those figures and nothing else. Do NOT add, subtract or infer capital expenditure:
that arithmetic is done outside, and only when disposals are pinned down.
"disposals_stated" is true only when the note actually says what disposals were, including when
it says there were none. If the note is silent about them, set it false and leave "disposals"
empty — a silent note is not a note saying zero.`

func (in FactsInput) prompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Borrower: %s (scenario %s)\n", in.Company, in.ScenarioID)
	fmt.Fprintf(&b, "Its transactions are identified as TXN-%s-NNNN.\n", in.ScenarioID)
	if len(in.MissingAmounts) > 0 {
		fmt.Fprintf(&b, "The ledger export left the amount of these rows BLANK: %s.\n"+
			"If any document below states what one of them was, that is a ledger_amount_fix.\n",
			strings.Join(in.MissingAmounts, ", "))
	}
	for _, d := range in.Documents {
		fmt.Fprintf(&b, "\n═══ document %s [%s] ═══\n%s\n", d.DocID, d.DocType, d.Text)
		if len(d.OCRPages) > 0 {
			fmt.Fprintf(&b, "[pages %v of this document have no text layer; OCR transcript follows]\n%s\n",
				d.OCRPages, d.OCRText)
		}
	}
	if g := in.GroupDoc; g != nil {
		fmt.Fprintf(&b, "\n═══ CONSOLIDATED STATEMENTS OF THE PARENT — document %s ═══\n"+
			"These cover the whole group, not this borrower alone. Read them only for the group\n"+
			"figures asked for below; they say nothing about this borrower's own adjustments or\n"+
			"ownership.\n%s\n%s\n", g.DocID, g.Text, _groupPPEInstruction)
	}
	return b.String()
}
