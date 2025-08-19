package httpapi

import (
	"bytes"
	"fmt"
	"errors" 
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

const maxUploadBytes = 5 << 20 // 5 MiB

// validateExtension проверяет только расширение.
func validateExtension(filename string) error {
	if ext := strings.ToLower(filepath.Ext(filename)); ext != ".xlsx" {
		return errors.New(string(ErrInvalidExt))
	}
	return nil
}

// openXLSX пытается открыть XLSX из байт.
func openXLSX(data []byte) (*excelize.File, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New(string(ErrInvalidXLSX)) 
	}
	return f, nil
}

// validateWorkbook проверяет наличие листов и минимум двух строк на первом листе.
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

// validateRows: пустые строки пропускаем; частично пустые — ошибка.
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
