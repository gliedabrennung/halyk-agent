package domain

import (
	"slices"
	"strings"
)

type Category string

const (
	CatRevenue Category = "revenue"

	CatFinancingReceipts Category = "financing_receipts"

	CatCapex Category = "capex"

	CatAssetTransfer Category = "asset_transfer"
	CatPayroll       Category = "payroll"
	CatUtilities     Category = "utilities"
	CatRent          Category = "rent"
	CatTaxes         Category = "taxes"

	CatInterestExpense Category = "interest_expense"

	CatInterestIncome      Category = "interest_income"
	CatInsurancePremiums   Category = "insurance_premiums"
	CatMarketing           Category = "marketing"
	CatProfessionalService Category = "professional_services"
	CatTelecom             Category = "telecom"

	CatOperatingCosts Category = "operating_costs"

	CatOtherOperating Category = "other_operating"

	CatOtherIncome Category = "other_income"

	CatUnknown Category = "unknown"
)

var Categories = []Category{
	CatRevenue, CatFinancingReceipts, CatCapex, CatAssetTransfer,
	CatPayroll, CatUtilities, CatRent, CatTaxes,
	CatInterestExpense, CatInterestIncome, CatInsurancePremiums,
	CatMarketing, CatProfessionalService, CatTelecom, CatOperatingCosts,
	CatOtherOperating, CatOtherIncome, CatUnknown,
}

func ValidCategory(c Category) bool { return slices.Contains(Categories, c) }

var _operatingCategories = map[Category]bool{
	CatMarketing:           true,
	CatProfessionalService: true,
	CatTelecom:             true,
	CatOperatingCosts:      true,
	CatOtherOperating:      true,
}

func (c Category) IsOperating() bool { return _operatingCategories[c] }

var _lineAliases = []struct {
	needle string
	cat    Category
}{
	{"переданных", CatAssetTransfer},
	{"дочерним организациям", CatAssetTransfer},
	{"операционн", CatOperatingCosts},
	{"эксплуатацион", CatOperatingCosts},
	{"ремонт", CatOperatingCosts},
	{"обслуживан", CatOperatingCosts},
	{"капитальн", CatCapex},
	{"капиталовложен", CatCapex},
	{"выручк", CatRevenue},
	{"доход от реализац", CatRevenue},
	{"оплату труда", CatPayroll},
	{"заработн", CatPayroll},
	{"персонал", CatPayroll},
	{"коммунальн", CatUtilities},
	{"аренд", CatRent},
	{"налог", CatTaxes},
	{"процентн", CatInterestExpense},
	{"страхов", CatInsurancePremiums},
	{"маркетинг", CatMarketing},
	{"реклам", CatMarketing},
	{"консультацион", CatProfessionalService},
	{"юридическ", CatProfessionalService},
	{"телеком", CatTelecom},
	{"финансирован", CatFinancingReceipts},
}

func CategoryForLine(line string) (Category, bool) {
	l := strings.ToLower(strings.TrimSpace(line))
	if l == "" {
		return CatUnknown, false
	}
	best, bestLen := CatUnknown, 0
	for _, a := range _lineAliases {
		if len(a.needle) > bestLen && strings.Contains(l, a.needle) {
			best, bestLen = a.cat, len(a.needle)
		}
	}
	if bestLen > 0 {
		return best, true
	}
	if c := Category(l); ValidCategory(c) {
		return c, true
	}
	return CatUnknown, false
}
