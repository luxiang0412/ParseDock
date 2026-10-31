// Package storage: untrusted input validation.
//
// HTTP Content-Type is never trusted. The declared extension, the sniffed magic
// bytes and (for OOXML) the archive's internal structure must agree; otherwise
// the upload is rejected before any parser sees it.
package storage

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Format describes an accepted input type.
type Format struct {
	Name     string // pdf, docx, xlsx, pptx, csv, tsv, txt, md, png, jpeg, webp
	Mime     string
	Exts     []string
	NeedOCR  bool
	ZipBased bool
	Magic    func([]byte) bool
}

func hasPrefix(p string) func([]byte) bool {
	return func(b []byte) bool { return bytes.HasPrefix(b, []byte(p)) }
}

// pdfMagic accepts the header within the first 1024 bytes: the PDF specification
// requires it at offset 0, but real-world files (and Adobe Reader) tolerate a
// short prefix. Verified on the test corpus: 2 files carry a wrapper preamble.
var pdfMagic = func(b []byte) bool {
	window := b
	if len(window) > 1024 {
		window = window[:1024]
	}
	return bytes.Contains(window, []byte("%PDF-"))
}
var pngMagic = func(b []byte) bool {
	return len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
}
var jpegMagic = func(b []byte) bool { return len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF }
var webpMagic = func(b []byte) bool {
	return len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}
var zipMagic = func(b []byte) bool { return len(b) >= 4 && bytes.Equal(b[:4], []byte{'P', 'K', 0x03, 0x04}) }

// ole2Magic is the compound-file signature shared by legacy .doc/.xls/.ppt.
var ole2Magic = func(b []byte) bool {
	return len(b) >= 8 && bytes.Equal(b[:8], []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1})
}

// Formats is the upload allow-list (P0).
var Formats = []Format{
	{Name: "pdf", Mime: "application/pdf", Exts: []string{".pdf"}, Magic: pdfMagic},
	{Name: "docx", Mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Exts: []string{".docx"}, ZipBased: true, Magic: zipMagic},
	{Name: "xlsx", Mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Exts: []string{".xlsx", ".xlsm"}, ZipBased: true, Magic: zipMagic},
	{Name: "pptx", Mime: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Exts: []string{".pptx"}, ZipBased: true, Magic: zipMagic},
	// Legacy OLE2 Office. Text and tables only: no page geometry, no bbox.
	{Name: "doc", Mime: "application/msword", Exts: []string{".doc", ".dot"}, Magic: ole2Magic},
	{Name: "xls", Mime: "application/vnd.ms-excel", Exts: []string{".xls", ".xlt"}, Magic: ole2Magic},
	{Name: "ppt", Mime: "application/vnd.ms-powerpoint", Exts: []string{".ppt", ".pot"}, Magic: ole2Magic},
	{Name: "csv", Mime: "text/csv", Exts: []string{".csv"}},
	{Name: "tsv", Mime: "text/tab-separated-values", Exts: []string{".tsv"}},
	{Name: "txt", Mime: "text/plain", Exts: []string{".txt"}},
	{Name: "md", Mime: "text/markdown", Exts: []string{".md", ".markdown"}},
	{Name: "png", Mime: "image/png", Exts: []string{".png"}, NeedOCR: true, Magic: pngMagic},
	{Name: "jpeg", Mime: "image/jpeg", Exts: []string{".jpg", ".jpeg"}, NeedOCR: true, Magic: jpegMagic},
	{Name: "webp", Mime: "image/webp", Exts: []string{".webp"}, NeedOCR: true, Magic: webpMagic},
}

// LookupByExt finds a format by filename extension.
func LookupByExt(name string) (Format, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	for _, f := range Formats {
		for _, e := range f.Exts {
			if e == ext {
				return f, true
			}
		}
	}
	return Format{}, false
}

// LookupByName finds a format by canonical name.
func LookupByName(name string) (Format, bool) {
	for _, f := range Formats {
		if f.Name == name {
			return f, true
		}
	}
	return Format{}, false
}

// MustLookupByName panics on unknown name (programmer error).
func MustLookupByName(name string) Format {
	f, ok := LookupByName(name)
	if !ok {
		panic("unknown format: " + name)
	}
	return f
}

// ValidationError carries a stable error code for the API layer.
type ValidationError struct {
	Code string
	Msg  string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Msg }

// Limits bound untrusted archives and documents.
type Limits struct {
	MaxBytes           int64
	MaxPages           int
	MaxZipUncompressed int64
	MaxZipRatio        float64
	MaxZipEntries      int
}

// DefaultLimits are intentionally conservative; tune only with measurements.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes:           64 << 20,
		MaxPages:           500,
		MaxZipUncompressed: 512 << 20,
		MaxZipRatio:        200,
		MaxZipEntries:      5000,
	}
}

// SniffFormat determines the format from the first bytes plus the filename.
// It returns the format and a normalized display MIME type.
func SniffFormat(fileName string, head []byte, lim Limits) (Format, error) {
	if len(head) == 0 {
		return Format{}, &ValidationError{Code: "malformed_file", Msg: "empty file"}
	}
	byExt, hasExt := LookupByExt(fileName)

	// OLE2 is a shared container (doc/xls/ppt), so like zip its type comes from the
	// extension; VerifyCompoundFile then validates the header before an engine runs.
	if ole2Magic(head) {
		if hasExt && isOLE2Format(byExt.Name) {
			return byExt, nil
		}
		return Format{}, &ValidationError{
			Code: "unsupported_type",
			Msg:  "OLE2 compound files are only accepted as .doc/.dot/.xls/.xlt/.ppt/.pot",
		}
	}

	// A zip container is shared by docx/xlsx/pptx, so its real type must come
	// from the extension and is confirmed later by VerifyZipContainer.
	if zipMagic(head) {
		if hasExt && byExt.ZipBased {
			return byExt, nil
		}
		return Format{}, &ValidationError{
			Code: "unsupported_type",
			Msg:  "zip archives are not accepted (only docx/xlsx/pptx by extension)",
		}
	}

	// Walk the magic table for non-zip formats.
	var magicMatch *Format
	for i := range Formats {
		f := &Formats[i]
		if f.ZipBased {
			continue
		}
		if f.Magic != nil && f.Magic(head) {
			magicMatch = f
			break
		}
	}
	if magicMatch != nil {
		if hasExt && byExt.Name != magicMatch.Name {
			return Format{}, &ValidationError{
				Code: "unsupported_type",
				Msg:  fmt.Sprintf("extension %q does not match file content (%s)", filepath.Ext(fileName), magicMatch.Name),
			}
		}
		return *magicMatch, nil
	}
	if hasExt && !byExt.ZipBased {
		// Formats with a binary signature must actually carry it; a text file
		// named .pdf/.png is rejected here rather than deep inside an engine.
		if byExt.Magic != nil {
			return Format{}, &ValidationError{
				Code: "unsupported_type",
				Msg: fmt.Sprintf("content does not match the %s signature for extension %q "+
					"(no %s header in the first 1024 bytes; the file may be corrupt or zero-filled)",
					byExt.Name, filepath.Ext(fileName), byExt.Name),
			}
		}
		if !looksLikeText(head) {
			return Format{}, &ValidationError{Code: "unsupported_type", Msg: "content is not text for a text extension"}
		}
		return byExt, nil
	}
	if !hasExt && looksLikeText(head) {
		return MustLookupByName("txt"), nil
	}
	return Format{}, &ValidationError{Code: "unsupported_type", Msg: "unrecognized file content"}
}

// VerifyZipContainer checks OOXML archives for the expected part and for
// compression-bomb characteristics before handing the file to an engine.
func VerifyZipContainer(path string, format Format, lim Limits) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return &ValidationError{Code: "malformed_file", Msg: "not a valid zip container"}
	}
	defer zr.Close()
	if len(zr.File) > lim.MaxZipEntries {
		return &ValidationError{Code: "suspicious_compression", Msg: "too many archive entries"}
	}
	var total int64
	var haveCore bool
	for _, f := range zr.File {
		total += int64(f.UncompressedSize64)
		if total > lim.MaxZipUncompressed {
			// Not a bomb signal but a real limit: the configured engines cannot
			// expand documents this large (xberg caps at 500 MiB, docling-rs at
			// 10M cells). Report it as a size problem so callers can act on it.
			return &ValidationError{
				Code: "expanded_too_large",
				Msg: fmt.Sprintf("expanded archive needs %d MiB (limit %d MiB); the parsers cannot process documents this large",
					total>>20, lim.MaxZipUncompressed>>20),
			}
		}
		// Bomb signal: an extreme ratio (>200:1 by default) on a part that is not
		// trivially small. Legitimate Excel XML compresses ~10-40x, so a real
		// deflate bomb is an order of magnitude away from the legit distribution.
		if f.CompressedSize64 > 0 && f.UncompressedSize64 > 1<<20 {
			ratio := float64(f.UncompressedSize64) / float64(f.CompressedSize64)
			if ratio > lim.MaxZipRatio {
				return &ValidationError{
					Code: "suspicious_compression",
					Msg:  fmt.Sprintf("compression ratio %.0f:1 on a %d MiB part exceeds the bomb threshold", ratio, f.UncompressedSize64>>20),
				}
			}
		}
		switch format.Name {
		case "docx":
			if f.Name == "word/document.xml" {
				haveCore = true
			}
		case "xlsx":
			if f.Name == "xl/workbook.xml" {
				haveCore = true
			}
		case "pptx":
			if f.Name == "ppt/presentation.xml" {
				haveCore = true
			}
		}
		if strings.Contains(f.Name, "..") || strings.HasPrefix(f.Name, "/") {
			return &ValidationError{Code: "malformed_file", Msg: "archive contains unsafe paths"}
		}
	}
	if !haveCore {
		return &ValidationError{Code: "unsupported_type", Msg: "archive does not match " + format.Name + " structure"}
	}
	return nil
}

func isOLE2Format(name string) bool {
	return name == "doc" || name == "xls" || name == "ppt"
}

// VerifyCompoundFile validates the OLE2 (compound file binary) header before an
// engine is invoked. Legacy Office files are untrusted input like any other: the
// parser does the real work, this only rejects non-CFB bytes and absurd headers.
func VerifyCompoundFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 512)
	if _, err := io.ReadFull(f, head); err != nil {
		return &ValidationError{Code: "malformed_file", Msg: "compound file shorter than its header"}
	}
	if !bytes.Equal(head[:8], []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) {
		return &ValidationError{Code: "malformed_file", Msg: "not an OLE2 compound file"}
	}
	if head[28] != 0xFE || head[29] != 0xFF {
		return &ValidationError{Code: "malformed_file", Msg: "compound file byte order marker is invalid"}
	}
	sectorShift := uint16(head[30]) | uint16(head[31])<<8
	major := uint16(head[26]) | uint16(head[27])<<8
	if (major == 3 && sectorShift != 9) || (major == 4 && sectorShift != 12) ||
		(major != 3 && major != 4) {
		return &ValidationError{Code: "malformed_file", Msg: "unsupported compound file version/sector size"}
	}
	return nil
}

// CheckPDF runs cheap structural checks and returns an approximate page count.
// It deliberately does not attempt full PDF parsing (the engine does that);
// the goal is to bound work before an expensive parser is invoked.
func CheckPDF(path string, lim Limits) (pages int, encrypted bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	// Read head and tail before judging: a PDF's trailer/`startxref` lives at the
	// end of the file, so checking only the first MiB rejected 30 % of a real
	// corpus as "markers missing" (found by the test_data run, PR-01 follow-up).
	buf := make([]byte, 1<<20)
	if _, err := io.ReadFull(f, buf); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, false, err
	}
	// The tail window must not overlap the head, or page objects in the overlap would be
	// counted twice (a 3 MiB PDF counted 1 MiB twice in the old layout).
	var tail []byte
	tailStart := st.Size() - (8 << 20)
	if tailStart < int64(len(buf)) {
		tailStart = int64(len(buf))
	}
	if tailStart < st.Size() {
		if _, err := f.Seek(tailStart, io.SeekStart); err == nil {
			tail = make([]byte, 8<<20)
			n, rerr := io.ReadFull(f, tail)
			tail = tail[:n]
			if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, io.EOF) {
				return 0, false, rerr
			}
		}
	}

	// Structural pre-flight: a real PDF must show at least one object/trailer
	// marker in the head or the tail. This rejects renamed binaries and
	// truncated garbage before an engine is invoked, without a full parse.
	has := func(marker []byte) bool {
		return bytes.Contains(buf, marker) || bytes.Contains(tail, marker)
	}
	if !has([]byte("%%EOF")) && !has([]byte("startxref")) && !has([]byte("/Root")) {
		return 0, false, &ValidationError{Code: "malformed_file", Msg: "PDF structure markers are missing"}
	}
	// Head and tail are adjacent when the file is smaller than head+tail: counting them
	// separately loses a "/Type /Page" token straddling the 1 MiB seam. Count a contiguous
	// window once instead.
	if len(tail) > 0 && tailStart == int64(len(buf)) {
		joined := make([]byte, 0, len(buf)+len(tail))
		joined = append(joined, buf...)
		joined = append(joined, tail...)
		pages = countPageObjects(joined)
	} else {
		pages = countPageObjects(buf) + countPageObjects(tail)
	}
	encrypted = has([]byte("/Encrypt"))
	if lim.MaxPages > 0 && pages > lim.MaxPages {
		return pages, encrypted, &ValidationError{Code: "page_limit_exceeded", Msg: fmt.Sprintf("document has ~%d pages (limit %d)", pages, lim.MaxPages)}
	}
	return pages, encrypted, nil
}

// countPageObjects counts /Type /Page (but not the /Type /Pages tree node).
// It is a cheap upper-bound heuristic used only for pre-flight limits; the
// engine reports the authoritative page count.
func countPageObjects(b []byte) int {
	n := 0
	needle := []byte("/Type")
	for {
		i := bytes.Index(b, needle)
		if i < 0 {
			return n
		}
		rest := bytes.TrimLeft(b[i+len(needle):], " \t\r\n")
		if bytes.HasPrefix(rest, []byte("/Page")) {
			after := rest[len("/Page"):]
			if len(after) == 0 || (after[0] != 's' && after[0] != 'L') {
				n++
			}
		}
		b = b[i+len(needle):]
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// looksLikeText rejects binary content that lies about its extension.
func looksLikeText(head []byte) bool {
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	sample := head
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	var printable int
	for _, b := range sample {
		switch {
		case b >= 0x20 && b < 0x7f:
			printable++
		case b == '\n' || b == '\r' || b == '\t' || b >= 0x80: // allow UTF-8 / GB18030 bytes
			printable++
		}
	}
	return float64(printable)/float64(len(sample)) > 0.85
}
