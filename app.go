package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/bizipoopoo/pons-mm-desktop/internal/control"
	"github.com/bizipoopoo/pons-mm-desktop/internal/vault"
)

// App is the narrow Wails binding surface. Secrets are accepted only by vault
// methods and are never returned except for a newly generated recovery phrase.
type App struct {
	ctx     context.Context
	service *control.Service
}

func NewApp() (*App, error) {
	dataDir, err := control.DefaultDataDir()
	if err != nil {
		return nil, err
	}
	service, err := control.NewService(dataDir)
	if err != nil {
		return nil, err
	}
	return &App{service: service}, nil
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.service.SetRuntime(ctx, func(name string, value any) {
		runtime.EventsEmit(ctx, name, value)
	})
	// Startup initialization: verify the gate wallet balance on the public
	// Robinhood node. The process terminates when the gate does not pass.
	go a.enforceInitGate()
}

// enforceInitGate runs the startup initialization check and kills the process
// when it does not pass. A definitive underfunded balance exits immediately;
// an unreachable node is retried a few times, then the gate fails closed.
func (a *App) enforceInitGate() {
	const attempts = 5
	for attempt := 1; ; attempt++ {
		status := a.service.RunInitCheck()
		if status.OK {
			return
		}
		// BalanceETH is only set when the balance read itself succeeded, so a
		// non-empty value means the wallet is genuinely below the threshold.
		if status.BalanceETH != "" || attempt >= attempts {
			os.Exit(1)
		}
		time.Sleep(3 * time.Second)
	}
}

func (a *App) shutdown(context.Context) { a.service.Shutdown() }

func (a *App) Bootstrap() control.Bootstrap  { return a.service.Bootstrap() }
func (a *App) NewStrategy() control.Strategy { return a.service.NewStrategy() }

func (a *App) SaveSettings(settings control.Settings) error {
	return a.service.SaveSettings(settings)
}

func (a *App) SaveStrategy(strategy control.Strategy) (control.Strategy, error) {
	return a.service.SaveStrategy(strategy)
}

func (a *App) DeleteStrategy(id string) error { return a.service.DeleteStrategy(id) }

func (a *App) CreateVault(password string) error { return a.service.CreateVault(password) }
func (a *App) UnlockVault(password string) error { return a.service.UnlockVault(password) }
func (a *App) LockVault() error                  { return a.service.LockVault() }

func (a *App) ImportPrivateKeys(input, labelPrefix string) ([]vault.Summary, error) {
	return a.service.ImportPrivateKeys(input, labelPrefix)
}

func (a *App) ImportMnemonic(mnemonic string, count int, labelPrefix string) ([]vault.Summary, error) {
	return a.service.ImportMnemonic(mnemonic, count, labelPrefix)
}

func (a *App) GenerateMnemonic() (string, error) { return a.service.GenerateMnemonic() }

func (a *App) PreflightStrategy(id string) (string, error) { return a.service.Preflight(id) }
func (a *App) StartStrategy(id, confirmation string) error { return a.service.Start(id, confirmation) }
func (a *App) StopStrategy(id string) error                { return a.service.Stop(id) }
func (a *App) ExitStrategy(id, confirmation string) error  { return a.service.ExitAll(id, confirmation) }

// RunInitCheck re-runs the startup initialization gate on demand.
func (a *App) RunInitCheck() control.InitStatus { return a.service.RunInitCheck() }

// ResetStrategy clears a fully-sold strategy's launched token so the same
// configuration can immediately launch and test a fresh token.
func (a *App) ResetStrategy(id string) (control.Strategy, error) { return a.service.ResetStrategy(id) }

// FetchLatestLaunch returns the newest factory launch's metadata for
// prefilling a strategy's token settings.
func (a *App) FetchLatestLaunch() (control.LaunchPreset, error) { return a.service.FetchLatestLaunch() }

// GenerateFundingWallets creates one funding wallet role (deposit cold,
// deposit relays, or withdraw relays), stores the keys in the vault, and
// forces a backup download before the generation is considered complete.
func (a *App) GenerateFundingWallets(role string) (string, error) {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save the funding wallet backup (required)",
		DefaultFilename: control.FundingExportFilename(role),
		Filters:         []runtime.FileFilter{{DisplayName: "JSON", Pattern: "*.json"}},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", errors.New("generation cancelled: the wallet backup download is required")
	}
	if filepath.Ext(path) == "" {
		path += ".json"
	}
	export, err := a.service.GenerateFundingWallets(role)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(export.Payload), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) SetFundingWithdrawCold(address string) error {
	return a.service.SetFundingWithdrawCold(address)
}

func (a *App) FundingState() control.FundingState { return a.service.FundingState() }

func (a *App) CreateFundingTask(kind, input string) (control.FundingTask, error) {
	return a.service.CreateFundingTask(kind, input)
}

// ExportFundingBatches downloads the five temporary batch mnemonics with the
// derived per-slot addresses for one task.
func (a *App) ExportFundingBatches(taskID string) (string, error) {
	export, err := a.service.ExportFundingBatches(taskID)
	if err != nil {
		return "", err
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save the batch mnemonics",
		DefaultFilename: export.Filename,
		Filters:         []runtime.FileFilter{{DisplayName: "JSON", Pattern: "*.json"}},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", errors.New("export cancelled")
	}
	if filepath.Ext(path) == "" {
		path += ".json"
	}
	if err := os.WriteFile(path, []byte(export.Payload), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) StartFundingTask(id, confirmation string) error {
	return a.service.StartFundingTask(id, confirmation)
}

func (a *App) StopFundingTask(id string) error   { return a.service.StopFundingTask(id) }
func (a *App) DeleteFundingTask(id string) error { return a.service.DeleteFundingTask(id) }

func (a *App) ExportGMGN(id string) (string, error) {
	payload, err := a.service.GMGNImport(id)
	if err != nil {
		return "", err
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export GMGN wallet tags",
		DefaultFilename: "ponsdesk-gmgn-import.json",
		Filters:         []runtime.FileFilter{{DisplayName: "JSON", Pattern: "*.json"}},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", errors.New("export cancelled")
	}
	if filepath.Ext(path) == "" {
		path += ".json"
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
