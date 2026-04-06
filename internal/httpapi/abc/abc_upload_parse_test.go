package abc

import "testing"

func TestParseHeader_PrefersProductNameAndQuantityOverBarcode(t *testing.T) {
	header := []string{
		"Наименование организации (дилер)",
		"Наименование товара (дилер)",
		"Штрихкод EAN (дилер)",
		"Кол-во",
		"Цена продажи",
		"Сумма продажи",
	}

	idx, err := parseHeader(header)
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if idx.name != 1 {
		t.Fatalf("name index = %d, want 1", idx.name)
	}
	if idx.qty != 3 {
		t.Fatalf("qty index = %d, want 3", idx.qty)
	}
	if idx.price != 4 {
		t.Fatalf("price index = %d, want 4", idx.price)
	}
	if idx.value != 5 {
		t.Fatalf("value index = %d, want 5", idx.value)
	}
}

func TestParseHeader_QuantityShortUnitSupported(t *testing.T) {
	header := []string{"Наименование товара", "шт", "Цена продажи"}

	idx, err := parseHeader(header)
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if idx.name != 0 {
		t.Fatalf("name index = %d, want 0", idx.name)
	}
	if idx.qty != 1 {
		t.Fatalf("qty index = %d, want 1", idx.qty)
	}
	if idx.price != 2 {
		t.Fatalf("price index = %d, want 2", idx.price)
	}
}
