# PonsDesk

PonsDesk is a native desktop control plane for launching and operating Pons v1 and v2 market-making strategies on Robinhood Chain (chain ID `4663`). The trading and monitoring engine is written in Go; the desktop shell uses Wails and React.

> This application signs and broadcasts real transactions. Market making can lose all deployed capital through adverse price movement, gas, slippage, contract behavior, or software failure. Review every strategy and run a read-only preflight before enabling live execution.

![PonsDesk overview](docs/ponsdesk-overview.png)

## Highlights

- Run several token/pool strategies concurrently with independent RPC clients and logs.
- Prevent active strategies from sharing a wallet, avoiding cross-strategy nonce collisions.
- Launch a new Pons v2 bonding curve or a legacy Pons v1 token, and bind existing v2 curve or v1 Uniswap V3 venues.
- Configure token metadata, accumulation, distribution, oscillation, slippage, gas reserve, and priority tip from the UI.
- Import newline-separated private keys or derive EVM accounts from a BIP-39 mnemonic at `m/44'/60'/0'/0/i`.
- Encrypt all imported keys locally with scrypt + AES-256-GCM. Mnemonics are never persisted.
- Generate secret-free GMGN bulk-import JSON for selected strategy wallets.
- Build desktop bundles for macOS and Windows through GitHub Actions.

## Downloads

Download the latest macOS universal application or Windows x64 installer from [GitHub Releases](https://github.com/bizipoopoo/pons-mm-desktop/releases).

The release workflow signs macOS builds with a Developer ID Application certificate, notarizes them with Apple, and staples the ticket before packaging. Releases `v0.1.1` and earlier were ad-hoc signed and may require approval under **System Settings > Privacy & Security** before their first launch.

## First Run

1. Open **Settings** and configure a Robinhood Chain RPC. A `wss://` endpoint enables event-driven pool monitoring; HTTPS falls back to polling.
2. Open **Wallet vault**, create a vault password, and import private keys or a mnemonic.
3. Create a strategy. The first assigned wallet is treasury/deployer; remaining wallets are makers.
4. Run **Preflight**. It performs RPC reads and launch/binding checks without sending a transaction.
5. Start the strategy and type `LIVE` in the explicit confirmation dialog.

After a successful launch, PonsDesk automatically persists the emitted token and venue address (v2 curve or v1 pool) and switches that strategy to **Existing pair**. Restarting the strategy will not launch a second token.

## Pons Protocol Versions

New strategies default to the current Pons v2 stack. Existing configurations created before v2 desktop support remain on v1 until explicitly changed.

| Protocol | Launch and trading path | Desktop behavior |
| --- | --- | --- |
| v2 | Bonding curve, then graduation to Uniswap V4 | Launches and market-makes the native-ETH curve; maker wallets are included as opening-tax exemptions. The engine stops safely at graduation and leaves any remaining positions for a future/manual V4 route. |
| v1 | Direct launch into a locked Uniswap V3 pool | Preserves the existing V3 market-making flow. Launching may require a whitelisted deployer while the v1 public gate is closed. |

The v2 direct-launch path does not offer an atomic creator initial buy. Set **Initial buy** to zero; maker accumulation begins after launch confirmation. Custom ERC-20 quote curves are not supported by the desktop engine yet.

## Multi-Pair Safety

Each running strategy owns its own Pons client, context, engine, and selected wallet set. PonsDesk rejects a start request when any selected wallet is already reserved by another live strategy. Assign disjoint treasury and maker wallets to every concurrently active pair.

The vault must remain unlocked while strategies are running. Private keys are copied into in-memory signers only for selected strategies and are never written to a plaintext key file.

## GMGN Tags

GMGN does not currently expose an official write API for followed-wallet remarks. PonsDesk exports its supported bulk-import JSON through the download button in the strategy row. Import that file once from the GMGN website while logged into your chosen viewer account.

## Development

Requirements:

- Go `1.25+`
- Node.js `22+`
- Wails CLI `v2.14.0`
- Platform dependencies listed in the [Wails installation guide](https://wails.io/docs/gettingstarted/installation)

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.14.0
cd frontend && npm ci && cd ..
wails dev
```

Run all checks:

```bash
go test ./...
go vet ./...
npm --prefix frontend run build
wails build -clean -trimpath
```

Application data is stored outside the repository:

| Platform | Default directory |
| --- | --- |
| macOS | `~/Library/Application Support/PonsDesk` |
| Windows | `%AppData%\PonsDesk` |

`config.json` contains non-wallet strategy configuration and wallet IDs. `wallets.vault` contains the encrypted wallet payload. Back up the recovery phrase and vault password separately; PonsDesk cannot recover either.

## Releases

Every version tag automatically builds macOS universal and Windows x64 desktop artifacts and publishes a GitHub Release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The macOS release job deliberately fails instead of publishing an ad-hoc-signed build when any Apple credential is missing. Configure these GitHub Actions repository secrets before creating a release tag:

| Secret | Value |
| --- | --- |
| `APPLE_CERTIFICATE_BASE64` | Base64-encoded Developer ID Application `.p12` export |
| `APPLE_CERTIFICATE_PASSWORD` | Password used when exporting the `.p12` file |
| `APPLE_ID` | Apple ID used for notarization |
| `APPLE_APP_SPECIFIC_PASSWORD` | App-specific password for that Apple ID |
| `APPLE_TEAM_ID` | 10-character Apple Developer team ID |

Generate the certificate secret on macOS with `base64 -i DeveloperIDApplication.p12 | pbcopy`. Keep the certificate and all credentials out of the repository.

Pushes and pull requests to `main` run Go tests, `go vet`, the frontend production build, and secret scanning.

## Architecture

```text
React UI <-> Wails bindings <-> control.Service
                                |-- encrypted vault
                                |-- persistent strategy store
                                `-- concurrent strategy jobs
                                      |-- ponsmm.Engine
                                      `-- pons.Client
```

The original protocol bindings and engine live in `internal/pons` and `internal/ponsmm`. Desktop orchestration lives in `internal/control`; key encryption and mnemonic derivation live in `internal/vault`.

## Security

- Never paste real private keys into issues, logs, screenshots, or repository files.
- Do not place funds in a GMGN viewer wallet.
- Use a dedicated RPC key and rotate it if an endpoint is accidentally committed.
- Validate downloaded release checksums and repository provenance.
- Report vulnerabilities privately according to [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
