package abc

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

type parseError struct {
	code ErrCode
	msg  string
}

func (e *parseError) Error() string { return e.msg }

type headerIndex struct {
	sku   int
	name  int
	value int
	price int
	qty   int
}

func parseHeader(header []string) (headerIndex, error) {
	idx := headerIndex{sku: -1, name: -1, value: -1, price: -1, qty: -1}
	for i, raw := range header {
		label := strings.ToLower(strings.TrimSpace(raw))
		switch label {
		case "#", "sku", "id", "code":
			if idx.sku < 0 {
				idx.sku = i
			}
		case "item", "name", "product":
			if idx.name < 0 {
				idx.name = i
			}
		case "value", "revenue", "amount", "total":
			if idx.value < 0 {
				idx.value = i
			}
		case "price", "unit_price", "price_unit", "unit price":
			if idx.price < 0 {
				idx.price = i
			}
		case "quantity", "qty", "count":
			if idx.qty < 0 {
				idx.qty = i
			}
		}
	}

	missing := []string{}
	if idx.name < 0 {
		missing = append(missing, "Item")
	}
	if idx.qty < 0 {
		missing = append(missing, "Quantity")
	}
	if idx.value < 0 && idx.price < 0 {
		missing = append(missing, "Value or Price")
	}
	if len(missing) > 0 {
		return idx, &parseError{
			code: ErrInvalidHeader,
			msg:  "missing required columns: " + strings.Join(missing, ", "),
		}
	}

	return idx, nil
}

func parseProducts(rows [][]string, idx headerIndex) ([]abclib.Product, error) {
	products := make([]abclib.Product, 0, len(rows))
	for i, row := range rows {
		rowNum := i + 2 // header is row 1
		if isRowEmpty(row) {
			continue
		}

		name := strings.TrimSpace(cellAt(row, idx.name))
		if name == "" {
			return nil, missingValueError(rowNum, idx.name)
		}

		qtyStr := cellAt(row, idx.qty)
		if strings.TrimSpace(qtyStr) == "" {
			return nil, missingValueError(rowNum, idx.qty)
		}
		qty, err := parseQuantity(qtyStr, rowNum, idx.qty)
		if err != nil {
			return nil, err
		}

		priceUnit, err := parsePriceUnit(row, idx, rowNum, qty)
		if err != nil {
			return nil, err
		}

		sku := ""
		if idx.sku >= 0 {
			sku = strings.TrimSpace(cellAt(row, idx.sku))
		}
		if sku == "" {
			sku = strconv.Itoa(rowNum - 1)
		}

		products = append(products, abclib.Product{
			SKU:      sku,
			Name:     name,
			Quantity: qty,
			Price:    priceUnit,
		})
	}

	if len(products) == 0 {
		return nil, &parseError{code: ErrNoData, msg: "xlsx has no data"}
	}
	return products, nil
}

func parsePriceUnit(row []string, idx headerIndex, rowNum int, qty int) (float64, error) {
	if idx.price >= 0 {
		priceStr := strings.TrimSpace(cellAt(row, idx.price))
		if priceStr != "" {
			priceUnit, err := parsePositiveFloat(priceStr, rowNum, idx.price)
			if err != nil {
				return 0, err
			}
			return priceUnit, nil
		}
	}

	if idx.value >= 0 {
		valueStr := strings.TrimSpace(cellAt(row, idx.value))
		if valueStr != "" {
			total, err := parsePositiveFloat(valueStr, rowNum, idx.value)
			if err != nil {
				return 0, err
			}
			return total / float64(qty), nil
		}
	}

	col := idx.price
	if col < 0 {
		col = idx.value
	}
	return 0, missingValueError(rowNum, col)
}

func parseQuantity(raw string, rowNum int, col int) (int, error) {
	value, err := parseFloat(raw)
	if err != nil {
		return 0, invalidNumberError(rowNum, col)
	}
	if value <= 0 || math.Mod(value, 1.0) != 0 {
		return 0, invalidQuantityError(rowNum, col)
	}
	return int(value), nil
}

func parsePositiveFloat(raw string, rowNum int, col int) (float64, error) {
	value, err := parseFloat(raw)
	if err != nil {
		return 0, invalidNumberError(rowNum, col)
	}
	if value <= 0 {
		return 0, invalidValueError(rowNum, col)
	}
	return value, nil
}

func parseFloat(raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, " ", "")
	if strings.Contains(s, ",") && !strings.Contains(s, ".") {
		s = strings.ReplaceAll(s, ",", ".")
	}
	return strconv.ParseFloat(s, 64)
}

func isRowEmpty(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func cellAt(row []string, idx int) string {
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return row[idx]
}

func missingValueError(row, col int) error {
	return &parseError{
		code: ErrMissingValue,
		msg:  fmt.Sprintf("row %d has missing value in column %d", row, col+1),
	}
}

func invalidNumberError(row, col int) error {
	return &parseError{
		code: ErrInvalidNumber,
		msg:  fmt.Sprintf("row %d has invalid number in column %d", row, col+1),
	}
}

func invalidQuantityError(row, col int) error {
	return &parseError{
		code: ErrInvalidQuantity,
		msg:  fmt.Sprintf("row %d has invalid quantity in column %d", row, col+1),
	}
}

func invalidValueError(row, col int) error {
	return &parseError{
		code: ErrInvalidValue,
		msg:  fmt.Sprintf("row %d has invalid value in column %d", row, col+1),
	}
}
