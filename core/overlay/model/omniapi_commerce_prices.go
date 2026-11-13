package model

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strconv"
)

//go:embed commerce-prices.json
var omniCommercePricesJSON string

type OmniCommercePriceTable struct {
	Version string                   `json:"version"`
	Prices  map[string]map[int]int64 `json:"prices"`
}

func OmniCommercePrices() (OmniCommercePriceTable, error) {
	var table OmniCommercePriceTable
	if json.Unmarshal([]byte(omniCommercePricesJSON), &table) != nil {
		return table, errors.New("invalid embedded price table")
	}
	return table, table.Validate()
}
func (t OmniCommercePriceTable) Validate() error {
	if t.Version == "" || len(t.Prices) != 5 {
		return errors.New("incomplete price version")
	}
	for _, monthly := range []int64{1900, 3900, 6900, 9900, 19900} {
		row := t.Prices[strconv.FormatInt(monthly, 10)]
		if len(row) != 4 || row[1] != monthly {
			return errors.New("invalid monthly price")
		}
		previous, previousMonths := monthly, int64(1)
		for _, m := range []int{3, 6, 12} {
			p := row[m]
			if p <= previous || p%100 != 0 || p*previousMonths >= previous*int64(m) {
				return errors.New("longer terms must have lower monthly prices")
			}
			previous, previousMonths = p, int64(m)
		}
	}
	return nil
}
func (p OmniCommerceProfile) Price(monthly int64, months int) (int64, error) {
	var table OmniCommercePriceTable
	if p.PriceSnapshot == "" {
		table, _ = OmniCommercePrices()
	} else if json.Unmarshal([]byte(p.PriceSnapshot), &table) != nil {
		return 0, errors.New("invalid price snapshot")
	}
	if err := table.Validate(); err != nil {
		return 0, err
	}
	value, ok := table.Prices[strconv.FormatInt(monthly, 10)][months]
	if !ok {
		return 0, errors.New("unknown plan or duration")
	}
	return value, nil
}
