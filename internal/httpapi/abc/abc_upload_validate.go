// Package abc provides handlers, validation helpers, errors and metrics for ABC analysis XLSX uploads.
package abc

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

const maxUploadBytes = 5 << 20 // 5 MiB

// validateExtension checks that the file extension is .xlsx.
func validateExtension(filename string) error {
	if ext := strings.ToLower(filepath.Ext(filename)); ext != ".xlsx" {
		return errors.New(string(ErrInvalidExt))
	}
	return nil
}

// openXLSX opens a workbook from raw bytes and ensures it is a valid XLSX.
func openXLSX(data []byte) (*excelize.File, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New(string(ErrInvalidXLSX))
	}
	return f, nil
}

// validateWorkbook ensures there is at least one sheet and at least two rows
// (header + at least one data row).
func validateWorkbook(f *excelize.File) (sheet string, rows [][]string, err error) {
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return "", nil, errors.New(string(ErrNoSheets))
	}
	sheet = sheets[0]
	rows, err = f.GetRows(sheet)
	if err != nil || len(rows) < 2 {
		return "", nil, errors.New(string(ErrNoData))
	}
	return sheet, rows, nil
}

// validateRows skips fully empty rows and fails if any non-empty row contains
// an empty cell (reports 1-based row/column in the error message).
func validateRows(rows [][]string) error {
	for i, row := range rows[1:] { // пропускаем заголовок
		allEmpty := true
		for _, c := range row {
			if strings.TrimSpace(c) != "" {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			continue
		}
		for j, c := range row {
			if strings.TrimSpace(c) == "" {
				// сообщаем точные координаты
				return fmt.Errorf("%s:%d:%d", ErrMissingValue, i+2, j+1)
			}
		}
	}
	return nil
}
