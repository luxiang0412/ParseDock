package engines

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
)

// MinerU adapts the MinerU 4.1 CLI.
//
// Important licensing/architecture facts verified in Phase 0 (see
// docs/ENGINE-CANDIDATES.md):
//   - MinerU 4.1 runs only its "flash" tier locally. basic/standard/advanced
//     tiers are served by mineru.net, i.e. they upload the document to a third
//     party. ParseDock never uses those tiers.
//   - The CLI talks to a local daemon (`mineru server start`), which keeps
//     models loaded (GPU) and a document library on disk. Workers must run with
//     an ephemeral MINERU_HOME and clear it between tenants.
type MinerU struct {
	Bin       string
	Tier      string // flash only in local mode
	DeviceStr string
	VersionS  string
	Home      string // ephemeral MINERU_HOME
	Sandbox   string
	MaxRSSMB  int64
	Timeout   time.Duration
	MaxChars  int
}

// Name implements Engine.
func (m *MinerU) Name() string { return "mineru" }

// Version implements Engine.
func (m *MinerU) Version() string {
	if m.VersionS != "" {
		return m.VersionS
	}
	return "unknown"
}

// Device implements Engine.
func (m *MinerU) Device() string {
	if m.DeviceStr != "" {
		return m.DeviceStr
	}
	return "gpu"
}

// Formats implements Engine.
func (m *MinerU) Formats() []string {
	return []string{"pdf", "png", "jpeg", "webp"}
}

// Modes implements Engine.
func (m *MinerU) Modes() []string { return []string{"accurate"} }

var mineruPageRe = regexp.MustCompile(`<!--\s*page\s+(\d+)\s+of\s+(\d+)\s*-->`)

// Probe implements Engine: the local daemon must be reachable.
func (m *MinerU) Probe(ctx context.Context) error {
	env := []string{}
	if m.Home != "" {
		env = append(env, "MINERU_HOME="+m.Home)
	}
	res, err := RunCLI(ctx, CLISpec{Bin: m.Bin, Env: env, MaxOutput: 8192}, os.TempDir(), "server", "status")
	if err != nil {
		return fmt.Errorf("mineru server not reachable: %v", firstLine(string(res.Stderr)))
	}
	return nil
}

// daemonLogPath is where MinerU's daemon writes its operational log inside
// MINERU_HOME. It is the only per-task source of device information MinerU has.
func (m *MinerU) daemonLogPath() string {
	home := strings.TrimSpace(m.Home)
	if home == "" {
		// Deployments set MINERU_HOME for the daemon; reading the same variable
		// keeps the evidence working without a second knob.
		home = strings.TrimSpace(os.Getenv("MINERU_HOME"))
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "logs", "doclib.log")
}

// Parse implements Engine.
func (m *MinerU) Parse(ctx context.Context, req Request) (*documentir.Document, error) {
	timeout := m.Timeout
	if timeout == 0 {
		timeout = req.Timeout
	}
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	local := workFile(req.WorkDir, "engine-input."+req.Format)
	if err := copyFile(req.Path, local); err != nil {
		return nil, Errf(true, jobs.ErrInternal, "stage input: %v", err)
	}
	outFile := workFile(req.WorkDir, "mineru-out.md")
	tier := m.Tier
	if tier == "" || tier == "pipeline" || tier == "basic" || tier == "standard" || tier == "advanced" {
		// Only flash is local; anything else would silently call the cloud.
		tier = "flash"
	}
	maxChars := m.MaxChars
	if maxChars == 0 {
		maxChars = 4000000
	}
	args := []string{"parse", local, "--tier", tier, "--force",
		"--wait", fmt.Sprintf("%d", int(timeout.Seconds())), "--format", "markdown",
		"--limit", fmt.Sprintf("%d", maxChars), "--output", outFile}
	if strings.EqualFold(req.Format, "pdf") {
		// MinerU rejects --pages for images ("full-document mode").
		args = append(args, "--pages", "all")
	}
	env := []string{}
	if m.Home != "" {
		env = append(env, "MINERU_HOME="+m.Home)
	}
	if req.Progress != nil {
		req.Progress(5, "engine_start")
	}
	// Evidence: snapshot the daemon log before the task so start-up lines (which
	// may legitimately mention CUDAExecutionProvider) can never be mistaken for
	// this task's execution.
	logWindow := openDaemonWindow(m.daemonLogPath())
	res, err := RunCLI(ctx, CLISpec{Bin: m.Bin, Env: env, Sandbox: m.Sandbox, MaxOutput: 8 << 20, MaxRSSMB: m.MaxRSSMB},
		req.WorkDir, args...)
	if err != nil {
		if res != nil && res.TimedOut {
			return nil, Errf(true, jobs.ErrTimeout, "mineru timed out after %s", timeout)
		}
		if errors.Is(err, ErrOOM) {
			return nil, Errf(true, jobs.ErrEngineOOM,
				"mineru exceeded its memory ceiling while parsing this document")
		}
		if errors.Is(err, ErrSandboxUnavailable) {
			return nil, Errf(false, jobs.ErrEngineUnavailable, "engine sandbox requested but unavailable: %v", err)
		}
		code, msg := jobs.ErrMalformed, "mineru failed"
		if res != nil {
			code, msg = ClassifyCLIError(string(res.Stderr))
			msg = fmt.Sprintf("mineru exit %d: %s", res.ExitCode, msg)
		}
		return nil, &Error{Code: code, Message: msg}
	}
	raw, rerr := os.ReadFile(outFile)
	if rerr != nil {
		return nil, Errf(false, jobs.ErrInternal, "mineru produced no output: %v", rerr)
	}
	lines, haveWindow := logWindow.newLines()
	evidence := mineruExecution(lines, haveWindow, m.daemonLogPath())
	if evidence.Kind == ExecutionUnknown {
		// Nothing was written during the task (the daemon loaded its models
		// earlier). Start-up evidence may still rule a GPU *out*, never in.
		if startup, ok := logWindow.tailLines(256 << 10); ok {
			if cpu := mineruStartupExecution(startup); cpu.Kind != "" {
				evidence = cpu
			}
		}
	}
	req.Report(evidence)

	doc := documentir.New()
	doc.ParserEngine = m.Name()
	doc.ParserVersion = m.Version()
	doc.Source.Format = req.Format
	doc.Source.MimeType = req.MimeType
	doc.Source.SizeBytes = fileSize(req.Path)
	doc.Routing = documentir.Routing{
		RequestedMode: req.Mode, RequestedDevice: req.Device, ActualDevice: m.Device(),
		Engine: m.Name(), Reason: "mineru flash tier (local only)",
	}
	doc.Metrics.ParseMs = res.Elapsed.Milliseconds()
	if res.PeakRSSMB > 0 {
		rss := res.PeakRSSMB
		doc.Metrics.PeakRSSMB = &rss
	}
	m.liftPages(doc, string(raw), req.Format)
	doc.AddWarning(documentir.WarnNoCoordinates,
		"mineru flash output carries page markers but no per-element bounding boxes", "info")
	if strings.Contains(string(raw), "<!-- page ") && strings.Contains(string(raw), "truncated") {
		doc.AddWarning(documentir.WarnTruncated, "mineru output reports truncation", "warn")
	}
	return doc, nil
}

// liftPages splits MinerU's markdown on its page markers so each element gets a
// real page number; text structure inside a page is lifted as Markdown.
func (m *MinerU) liftPages(doc *documentir.Document, md, format string) {
	locs := mineruPageRe.FindAllStringSubmatchIndex(md, -1)
	kind := pageKind(format)
	if len(locs) == 0 {
		LiftMarkdown(doc, md)
		doc.PageKind = kind
		doc.PageCount = 1
		return
	}
	for i, loc := range locs {
		start := loc[1]
		end := len(md)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		pageNo := 0
		fmt.Sscanf(md[loc[2]:loc[3]], "%d", &pageNo)
		scratch := documentir.New()
		LiftMarkdown(scratch, md[start:end])
		remapTables(scratch, len(doc.Tables), len(doc.Elements))
		for _, el := range scratch.Elements {
			el.Loc = &documentir.SourceLocation{Kind: kind, Page: pageNo}
			doc.Elements = append(doc.Elements, el)
		}
		for _, t := range scratch.Tables {
			t.Loc = &documentir.SourceLocation{Kind: kind, Page: pageNo}
			doc.Tables = append(doc.Tables, t)
		}
	}
	doc.PageKind = kind
	doc.PageCount = len(locs)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
