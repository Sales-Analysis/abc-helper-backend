package abc

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/xuri/excelize/v2"
)

type uploadPrepareResponse struct {
	Status      string            `json:"status"`
	Preparation UploadPreparation `json:"preparation"`
}

// UploadPrepareHandler detects column mapping for uploaded XLSX file without running ABC analysis.
func UploadPrepareHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	file, fh, err := parseUploadFileFromRequest(w, r)
	if err != nil {
		writeUploadError(w, r.Header.Get("X-Request-ID"), err)
		return
	}
	defer func() { _ = file.Close() }()

	preparation, err := prepareUploadFromReader(r.Context(), fh.Filename, file, r.Header.Get("X-Request-ID"))
	if err != nil {
		writeUploadError(w, r.Header.Get("X-Request-ID"), err)
		return
	}

	writeOK(w, uploadPrepareResponse{
		Status:      "prepared",
		Preparation: preparation,
	})
}

func prepareUploadFromReader(
	ctx context.Context,
	filename string,
	reader io.Reader,
	requestID string,
) (UploadPreparation, error) {
	if err := validateExtension(filename); err != nil {
		return UploadPreparation{}, &parseError{code: ErrInvalidExt, msg: "only .xlsx allowed"}
	}
	reportUploadProgress(ctx, uploadJobStageValidating, 8)

	xls, err := openXLSX(reader)
	if err != nil {
		return UploadPreparation{}, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx format"}
	}
	defer func() { _ = xls.Close() }()

	return prepareUploadFromWorkbook(ctx, xls, requestID)
}

func prepareUploadFromFile(
	ctx context.Context,
	filename string,
	filePath string,
	requestID string,
) (UploadPreparation, error) {
	if err := validateExtension(filename); err != nil {
		return UploadPreparation{}, &parseError{code: ErrInvalidExt, msg: "only .xlsx allowed"}
	}
	reportUploadProgress(ctx, uploadJobStageValidating, 8)

	if uploadStreamingEnabled() {
		preparation, streamErr := prepareUploadFromFileStreaming(ctx, filePath, requestID)
		if streamErr == nil {
			return preparation, nil
		}
		if !shouldFallbackToExcelizeAfterStreaming(streamErr) {
			return UploadPreparation{}, streamErr
		}
		slog.Default().Warn("abc_upload_streaming_prepare_fallback",
			slog.String("request_id", strings.TrimSpace(requestID)),
			slog.String("filename", strings.TrimSpace(filename)),
			slog.String("err", strings.TrimSpace(streamErr.Error())),
		)
	}

	xls, err := openXLSXFile(filePath)
	if err != nil {
		return UploadPreparation{}, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx format"}
	}
	defer func() { _ = xls.Close() }()

	return prepareUploadFromWorkbook(ctx, xls, requestID)
}

func prepareUploadFromWorkbook(
	ctx context.Context,
	xls *excelize.File,
	requestID string,
) (UploadPreparation, error) {
	sample, err := readUploadWorkbookSample(xls)
	if err != nil {
		switch err.Error() {
		case string(ErrNoSheets):
			return UploadPreparation{}, &parseError{code: ErrNoSheets, msg: "xlsx has no sheets"}
		case string(ErrNoData):
			return UploadPreparation{}, &parseError{code: ErrNoData, msg: "xlsx has no data"}
		default:
			return UploadPreparation{}, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
		}
	}
	reportUploadProgress(ctx, uploadJobStageValidating, 16)
	reportUploadProgress(ctx, uploadJobStageParsingRows, 20)

	_, _, preparation, err := resolveFallbackPreparationFromSample(
		ctx,
		requestID,
		sample.Rows,
		sample.TotalRows,
		sample.TotalCols,
	)
	if err != nil {
		return UploadPreparation{}, err
	}
	if !preparation.hasDetectedColumns() {
		return UploadPreparation{}, &parseError{code: ErrInvalidHeader, msg: "cannot detect required columns"}
	}
	reportUploadProgress(ctx, uploadJobStageFinalizing, 96)
	return preparation, nil
}
