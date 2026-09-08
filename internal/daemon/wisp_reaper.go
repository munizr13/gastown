package daemon

import (
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	agentconfig "github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/reaper"
	"github.com/steveyegge/gastown/internal/util"
)

var errReaperDispatchNotStarted = errors.New("reaper dispatch did not start")

const (
	// defaultWispReaperInterval is the patrol interval. Set to 1h since reaping
	// is cleanup work, not latency-sensitive. Was 30m before Dog-driven refactor.
	defaultWispReaperInterval = 1 * time.Hour
	// Wisps older than this are reaped (closed). Configurable via formula var max_age.
	defaultWispMaxAge = 24 * time.Hour
	// Closed wisps older than this are permanently deleted. Formula var: purge_age.
	defaultWispDeleteAge = 7 * 24 * time.Hour
	// Alert threshold: if open wisp count exceeds this, the Dog should escalate.
	// Shared with `gt reaper run` warning. See reaper.DefaultAlertThreshold.
	wispAlertThreshold = reaper.DefaultAlertThreshold
	// Closed mail older than this is permanently deleted. Formula var: mail_delete_age.
	defaultMailDeleteAge = 7 * 24 * time.Hour
	// Issues stale longer than this are auto-closed. Formula var: stale_issue_age.
	defaultStaleIssueAge = 7 * 24 * time.Hour
)

// WispReaperConfig holds configuration for the wisp_reaper patrol.
type WispReaperConfig struct {
	Enabled      bool     `json:"enabled"`
	DryRun       bool     `json:"dry_run,omitempty"`
	IntervalStr  string   `json:"interval,omitempty"`
	MaxAgeStr    string   `json:"max_age,omitempty"`
	DeleteAgeStr string   `json:"delete_age,omitempty"`
	Databases    []string `json:"databases,omitempty"`
}

// wispReaperInterval returns the configured interval, or the default (1h).
func wispReaperInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispReaperInterval
}

// wispReaperMaxAge returns the configured max age, or the default (24h).
func wispReaperMaxAge(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.MaxAgeStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.MaxAgeStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispMaxAge
}

// wispDeleteAge returns the configured delete age, or the default (7 days).
func wispDeleteAge(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.DeleteAgeStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.DeleteAgeStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispDeleteAge
}

// reapWisps is the thin orchestrator for the wisp_reaper patrol.
// It dispatches a Dog, whose attached molecule records actual execution.
// The Dog reads the formula steps and calls `gt reaper` CLI helpers.
// Falls back inline only when the dispatch process provably never started.
func (d *Daemon) reapWisps() {
	if !d.canRunPatrol("wisp_reaper") {
		return
	}

	config := d.patrolConfig.Patrols.WispReaper
	maxAge := wispReaperMaxAge(d.patrolConfig)
	deleteAge := wispDeleteAge(d.patrolConfig)

	vars := map[string]string{
		"max_age":         maxAge.String(),
		"purge_age":       deleteAge.String(),
		"stale_issue_age": defaultStaleIssueAge.String(),
		"mail_delete_age": defaultMailDeleteAge.String(),
		"alert_threshold": fmt.Sprintf("%d", wispAlertThreshold),
	}

	if config.DryRun {
		vars["dry_run"] = "true"
	}
	if len(config.Databases) > 0 {
		vars["databases"] = strings.Join(config.Databases, ",")
	}

	if config.DryRun {
		d.logger.Printf("wisp_reaper: DRY RUN — reporting only, no changes will be made")
	}

	// Try dispatching to a Dog for formula-driven execution.
	if err := d.dispatchReaperDog(vars); err != nil {
		if holdErr := deacon.CheckPatrolAllowed(d.config.TownRoot); holdErr != nil {
			d.logger.Printf("wisp_reaper: no inline fallback: %v", holdErr)
			return
		}
		if !errors.Is(err, errReaperDispatchNotStarted) {
			// A failed process may already have attached work or started a Dog.
			// Its exit status cannot authorize a second execution of the purge.
			d.logger.Printf("wisp_reaper: dispatch outcome unconfirmed (%v), no inline fallback", err)
			return
		}
		d.logger.Printf("wisp_reaper: Dog dispatch failed (%v), running inline fallback", err)
		// Only inline execution needs a daemon-owned molecule. Successful
		// dispatch has its own Dog-owned steps and is not a completion receipt.
		mol := d.pourDogMolecule(constants.MolDogReaper, vars)
		defer mol.close()
		d.reapWispsInline(config, maxAge, deleteAge, mol)
		return
	}

	d.logger.Printf("wisp_reaper: dispatched to Dog for formula-driven execution")
}

// dispatchReaperDog dispatches the mol-dog-reaper formula to a Dog via gt sling.
func (d *Daemon) dispatchReaperDog(vars map[string]string) error {
	if err := deacon.CheckPatrolAllowed(d.config.TownRoot); err != nil {
		return err
	}

	args := []string{"sling", constants.MolDogReaper, "deacon/dogs"}
	for k, v := range vars {
		args = append(args, "--var", fmt.Sprintf("%s=%s", k, v))
	}

	cmd := exec.Command(d.gtPath, args...) //nolint:gosec // G204: d.gtPath resolved at daemon init via LookPath
	cmd.Dir = d.config.TownRoot
	// gt sling performs writes, so use mutation routing env: it preserves PATH
	// while stripping stale bd target selectors and derived Beads endpoint aliases.
	cmd.Env = bdMutationRoutingEnv(d.config.TownRoot)
	util.SetDetachedProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: gt sling: %v", errReaperDispatchNotStarted, err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("gt sling: %w", err)
	}
	return nil
}

// reapWispsInline is the fallback that runs the reaper cycle inline when
// Dog dispatch is unavailable. Delegates to the reaper package for SQL execution.
func (d *Daemon) reapWispsInline(config *WispReaperConfig, maxAge, deleteAge time.Duration, mol *dogMol) {
	if err := deacon.CheckPatrolAllowed(d.config.TownRoot); err != nil {
		d.logger.Printf("wisp_reaper: inline fallback skipped: %v", err)
		return
	}

	databases := config.Databases
	host := d.doltServerHost()
	if len(databases) == 0 {
		databases = reaper.DiscoverDatabases(host, d.doltServerPort())
	}
	if len(databases) == 0 {
		d.logger.Printf("wisp_reaper: no databases to reap")
		mol.failStep("scan", "no databases found")
		return
	}
	d.logger.Printf("wisp_reaper: scanning %d databases (inline fallback)", len(databases))
	mol.closeStep("scan", fmt.Sprintf("database candidates=%d; schema and operation results follow", len(databases)))

	dryRun := config.DryRun
	var totalReaped, totalMoleculeSteps, totalOpen, totalPurged, totalMailPurged, totalAutoClosed int

	// Step 2: Reap
	var reapStats reaperPhaseStats
	for _, dbName := range databases {
		db, err := d.openReaperDatabase(dbName, 10*time.Second)
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: reap admission error: %v", dbName, err)
			reapStats.errors++
			continue
		}
		if db == nil {
			reapStats.skipped++
			continue
		}
		result, err := reaper.Reap(db, dbName, maxAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: reap error: %v", dbName, err)
			reapStats.errors++
			continue
		}
		reapStats.returned++
		totalReaped += result.Reaped
		totalMoleculeSteps += result.MoleculeStepsClosed
		totalOpen += result.OpenRemain
		if result.Reaped > 0 || result.MoleculeStepsClosed > 0 {
			reapSummary := fmt.Sprintf("wisp_reaper: %s: reaped %d stale wisps", dbName, result.Reaped)
			if result.MoleculeStepsClosed > 0 {
				reapSummary += fmt.Sprintf(", closed %d molecule steps", result.MoleculeStepsClosed)
			}
			d.logger.Printf("%s, %d open remain", reapSummary, result.OpenRemain)
		}
	}
	reapStats.record(mol, "reap", dryRun, fmt.Sprintf("wisps=%d, molecule_steps=%d", totalReaped, totalMoleculeSteps))

	// Step 3: Purge
	var purgeStats reaperPhaseStats
	for _, dbName := range databases {
		db, err := d.openReaperDatabase(dbName, 30*time.Second)
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: purge admission error: %v", dbName, err)
			purgeStats.errors++
			continue
		}
		if db == nil {
			purgeStats.skipped++
			continue
		}
		result, err := reaper.Purge(db, dbName, deleteAge, defaultMailDeleteAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: purge error: %v", dbName, err)
			purgeStats.errors++
			continue
		}
		purgeStats.returned++
		totalPurged += result.WispsPurged
		totalMailPurged += result.MailPurged
		for _, a := range result.Anomalies {
			d.logger.Printf("wisp_reaper: %s: ANOMALY: %s", dbName, a.Message)
		}
	}
	purgeStats.record(mol, "purge", dryRun, fmt.Sprintf("wisps=%d, mail=%d", totalPurged, totalMailPurged))

	// Step 3b: Close plugin receipts (fast-track — 1h instead of 7d stale age)
	pluginReceiptAge := 1 * time.Hour
	var totalPluginClosed int
	var pluginReceiptStats reaperPhaseStats
	for _, dbName := range databases {
		db, err := d.openReaperDatabase(dbName, 10*time.Second)
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: plugin-receipts admission error: %v", dbName, err)
			pluginReceiptStats.errors++
			continue
		}
		if db == nil {
			pluginReceiptStats.skipped++
			continue
		}
		result, err := reaper.ClosePluginReceipts(db, dbName, pluginReceiptAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: plugin receipt close error: %v", dbName, err)
			pluginReceiptStats.errors++
			continue
		}
		pluginReceiptStats.returned++
		totalPluginClosed += result.Closed
		if result.Closed > 0 {
			d.logger.Printf("wisp_reaper: %s: closed %d plugin receipts", dbName, result.Closed)
		}
	}

	// Step 3c: Close plugin dispatch mails (daemon→dog instruction beads that are never closed)
	pluginDispatchAge := 1 * time.Hour
	var totalDispatchClosed int
	var pluginDispatchStats reaperPhaseStats
	for _, dbName := range databases {
		db, err := d.openReaperDatabase(dbName, 10*time.Second)
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: plugin-dispatches admission error: %v", dbName, err)
			pluginDispatchStats.errors++
			continue
		}
		if db == nil {
			pluginDispatchStats.skipped++
			continue
		}
		result, err := reaper.ClosePluginDispatches(db, dbName, pluginDispatchAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: plugin dispatch close error: %v", dbName, err)
			pluginDispatchStats.errors++
			continue
		}
		pluginDispatchStats.returned++
		totalDispatchClosed += result.Closed
		if result.Closed > 0 {
			d.logger.Printf("wisp_reaper: %s: closed %d plugin dispatches", dbName, result.Closed)
		}
	}

	// Step 4: Auto-close
	var autoCloseStats reaperPhaseStats
	for _, dbName := range databases {
		db, err := d.openReaperDatabase(dbName, 10*time.Second)
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: auto-close admission error: %v", dbName, err)
			autoCloseStats.errors++
			continue
		}
		if db == nil {
			autoCloseStats.skipped++
			continue
		}
		result, err := reaper.AutoClose(db, dbName, defaultStaleIssueAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: auto-close error: %v", dbName, err)
			autoCloseStats.errors++
			continue
		}
		autoCloseStats.returned++
		totalAutoClosed += result.Closed
	}
	autoCloseStats.record(mol, "auto-close", dryRun, fmt.Sprintf("issues=%d", totalAutoClosed))

	// Step 5: Report
	if totalOpen > wispAlertThreshold {
		d.logger.Printf("wisp_reaper: WARNING: %d open wisps exceed threshold %d — investigate wisp lifecycle",
			totalOpen, wispAlertThreshold)
	}
	summary := fmt.Sprintf("wisp_reaper: cycle returned — reaped=%d", totalReaped)
	if totalMoleculeSteps > 0 {
		summary += fmt.Sprintf(" molecule_steps_closed=%d", totalMoleculeSteps)
	}
	summary += fmt.Sprintf(" purged=%d mail_purged=%d plugin_closed=%d dispatch_closed=%d auto_closed=%d open=%d databases=%d dryRun=%v",
		totalPurged, totalMailPurged, totalPluginClosed, totalDispatchClosed, totalAutoClosed, totalOpen, len(databases), dryRun)
	summary += fmt.Sprintf(" reap={%s} purge={%s} plugin_receipts={%s} plugin_dispatches={%s} auto_close={%s}",
		reapStats, purgeStats, pluginReceiptStats, pluginDispatchStats, autoCloseStats)
	d.logger.Printf("%s", summary)
	mol.closeStep("report", summary)
}

// openReaperDatabase checks the live hold before each database operation.
// A nil database with no error means its schema was checked and is unsupported;
// query/connection failures remain errors rather than successful skips.
func (d *Daemon) openReaperDatabase(name string, timeout time.Duration) (*sql.DB, error) {
	if err := deacon.CheckPatrolAllowed(d.config.TownRoot); err != nil {
		return nil, err
	}
	if err := reaper.ValidateDBName(name); err != nil {
		return nil, err
	}
	db, err := reaper.OpenDB(d.doltServerHost(), d.doltServerPort(), name, timeout, timeout)
	if err != nil {
		return nil, err
	}
	ok, err := reaper.HasReaperSchema(db)
	if err != nil || !ok {
		db.Close()
		return nil, err
	}
	return db, nil
}

type reaperPhaseStats struct {
	returned int
	skipped  int
	errors   int
}

func (s reaperPhaseStats) String() string {
	return fmt.Sprintf("databases_returned=%d, schema_skipped=%d, errors=%d", s.returned, s.skipped, s.errors)
}

func (s reaperPhaseStats) record(mol *dogMol, step string, dryRun bool, counts string) {
	receipt := fmt.Sprintf("%s: dry_run=%t, %s, %s", step, dryRun, s, counts)
	if s.errors > 0 {
		mol.failStep(step, receipt)
	} else if s.returned == 0 {
		mol.closeStep(step, "skipped: no database operation returned; "+receipt)
	} else {
		mol.closeStep(step, "returned: "+receipt)
	}
}

// doltServerPort returns the configured Dolt server port.
func (d *Daemon) doltServerPort() int {
	if d.doltServer != nil {
		return d.doltServer.config.Port
	}
	if port := agentconfig.ResolveDoltPort(d.config.TownRoot); port > 0 {
		return port
	}
	return doltserver.DefaultPort
}

func (d *Daemon) doltServerHost() string {
	if d.doltServer != nil && d.doltServer.config.Host != "" {
		return d.doltServer.config.Host
	}
	if host := agentconfig.ResolveDoltHost(d.config.TownRoot); host != "" {
		return host
	}
	if cfg := doltserver.DefaultConfig(d.config.TownRoot); cfg.Host != "" {
		return cfg.Host
	}
	return "127.0.0.1"
}
