package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

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
