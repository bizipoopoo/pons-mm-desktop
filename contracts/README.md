# pons-mm contracts

`PonsMMRouter` is the desktop app's own launch router for pons v2 bonding
curves. It lets the chain, not the client, decide which maker buys count as
"at launch":

- `launch(terms, factory, officialRouter)` forwards a launch to the official
  pons contracts and records the L2 block (`ArbSys.arbBlockNumber()`) it
  landed in. The official factory therefore records the router as the token's
  deployer; the creator fee recipient and the opening-buy recipient are
  whatever the caller passes. Because the treasury is no longer the deployer
  it must be listed in `snipeTaxExemptions` alongside the makers.
- `buyAfterLaunch(curve, maxBlocksAfterLaunch, recipient)` fills only while
  the current L2 block is within that many blocks of the recorded launch
  block, otherwise it reverts with `Expired`. Buys are broadcast in the same
  JSON-RPC batch as the launch; a delayed one reverts instead of buying late.
- `deposit(operator)` / `withdraw(amount)` park maker ETH, and
  `launchAndBuyAtomic(terms, factory, officialRouter, buys)` runs the launch
  and every maker buy in ONE transaction from those deposits, so nothing can
  trade between the launch and the makers' fills.
- `buyWithin(curve, maxL2Block, recipient)` (v1) pins an absolute height.

No slippage is enforced on any path: the makers are declared snipe-tax-exempt
first buyers and the window (or atomicity) is the only limit.

The router is a UUPS proxy so entrypoints can be added without changing the
address the app is configured with. Storage is append-only across upgrades.

## Setup

Dependencies are not vendored. After cloning:

```sh
curl -L https://foundry.paradigm.xyz | bash && foundryup
cd contracts
forge install OpenZeppelin/openzeppelin-contracts@v5.4.0 --no-git
forge install OpenZeppelin/openzeppelin-contracts-upgradeable@v5.4.0 --no-git
forge install foundry-rs/forge-std --no-git
forge test
```

## Deploy / upgrade

```sh
export ROBINHOOD_RPC=https://...
export DEPLOYER_KEY=0x...
forge script script/Deploy.s.sol --rpc-url robinhood --broadcast
```

The script prints the proxy address; that address is what the app's
Settings page (`Launch router`) and `internal/pons/abi.go` (`MMRouter`)
point at. Upgrades use `script/Upgrade.s.sol` with the same owner key and
`ROUTER_PROXY` set to the proxy address. After changing the contract,
regenerate the app's ABI slice (`mmRouterABIJSON` in `internal/pons/abi.go`)
from `out/PonsMMRouter.sol/PonsMMRouter.json`.

Mainnet: proxy `0x1119cDed80b82CA4d732fD1bB20c13f5e9425F60`.
